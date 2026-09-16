package mysql_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	resSchema "github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// Class A (2026-09 convergence audit): parameter_group_id is stored and
// echoed by the platform and never applied — the database service's
// parameter-apply endpoint is an unimplemented stub. The plan-time validator
// (internal/unenacted) refuses every known configuration value; these tests
// drive the Create/Update belts that stop an apply-resolved value from
// reaching the API, and assert the refusal names the real constraint.

// instancePlanWithParameterGroup builds a model whose storage/flavor match
// across plan and state, so the belt is the only thing that can fire.
func instancePlanWithParameterGroup(t *testing.T, parameterGroupValue types.String) MysqlInstanceModel {
	t.Helper()
	return MysqlInstanceModel{
		ID:               types.StringValue("db-123"),
		Name:             types.StringValue("my-db"),
		Version:          types.StringValue("8"),
		FlavorID:         types.StringValue("db.gp1.small"),
		StorageGB:        types.Int64Value(50),
		VPCID:            types.StringValue("vpc-1"),
		SubnetID:         types.StringValue("sn-1"),
		ParameterGroupID: parameterGroupValue,
		Status:           types.StringValue("running"),
		CreatedAt:        types.StringValue("2025-01-01T00:00:00Z"),
	}
}

func TestSchemaRefusesParameterGroupAtPlanTime(t *testing.T) {
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	attr, ok := schemaResp.Schema.Attributes["parameter_group_id"].(resSchema.StringAttribute)
	if !ok {
		t.Fatal("expected a String attribute parameter_group_id")
	}
	if len(attr.Validators) != 1 {
		t.Fatalf("expected the unenacted refusal validator on parameter_group_id, got %d validators", len(attr.Validators))
	}

	resp := &validator.StringResponse{}
	attr.Validators[0].ValidateString(context.Background(), validator.StringRequest{
		Path:        path.Root("parameter_group_id"),
		ConfigValue: types.StringValue("pg-1"),
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a known parameter_group_id value to be refused at plan validation")
	}
	if got, want := resp.Diagnostics.Errors()[0].Summary(), parameterGroupRefusalTitle; got != want {
		t.Errorf("expected summary %q, got %q", want, got)
	}
}

func TestCreateRefusesParameterGroup(t *testing.T) {
	requested := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requested.Store(true)
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &mysqlInstanceResource{client: c}

	plan := buildMysqlInstancePlan(t, instancePlanWithParameterGroup(t, types.StringValue("pg-1")))

	createResp := resource.CreateResponse{State: emptyMysqlInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected the create carrying parameter_group_id to be refused")
	}
	diag := createResp.Diagnostics.Errors()[0]
	if got, want := diag.Summary(), parameterGroupRefusalTitle; got != want {
		t.Errorf("expected summary %q, got %q", want, got)
	}
	if requested.Load() {
		t.Error("the refusal must fire before any API request")
	}
}

func TestUpdateRefusesParameterGroup(t *testing.T) {
	requested := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requested.Store(true)
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &mysqlInstanceResource{client: c}

	plan := buildMysqlInstancePlan(t, instancePlanWithParameterGroup(t, types.StringValue("pg-2")))
	state := buildMysqlInstanceState(t, instancePlanWithParameterGroup(t, types.StringValue("pg-1")))

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)

	if !updateResp.Diagnostics.HasError() {
		t.Fatal("expected the update carrying parameter_group_id to be refused")
	}
	if got, want := updateResp.Diagnostics.Errors()[0].Summary(), parameterGroupRefusalTitle; got != want {
		t.Errorf("expected summary %q, got %q", want, got)
	}
	if requested.Load() {
		t.Error("the refusal must fire before any API request")
	}
}

// TestUpdateDrainsStoredParameterGroup mirrors the postgres drain: the
// attribute is omitted from the configuration, the belt does not fire, and
// the PUT carries the clearing value. Platform-side, the database service's
// UpdateInstanceRequest.Validate never inspects parameterGroupId and the
// repository write maps "" through nullString to SQL NULL (see the postgres
// refusal test's comment for the measured chain) — the drain cannot wedge.
func TestUpdateDrainsStoredParameterGroup(t *testing.T) {
	var putBody apiUpdateMysqlInstanceRequest
	putCalled := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/databases/db-123":
			putCalled.Store(true)
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Errorf("PUT body decode failed: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/db-123":
			_ = json.NewEncoder(w).Encode(apiMysqlInstance{
				ID: "db-123", Name: "my-db", Type: "mysql", TypeVersion: "8.4",
				FlavorID: "db.gp1.small", StorageGB: 50, VPCID: "vpc-1", SubnetID: "sn-1",
				Status: "running", Port: 3306, CreatedAt: "2025-01-01T00:00:00Z",
			})
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &mysqlInstanceResource{
		client:       c,
		pollInterval: 10 * time.Millisecond,
		pollTimeout:  2 * time.Second,
	}

	plan := buildMysqlInstancePlan(t, instancePlanWithParameterGroup(t, types.StringNull()))
	state := buildMysqlInstanceState(t, instancePlanWithParameterGroup(t, types.StringValue("pg-1")))

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), resource.UpdateRequest{Plan: plan, State: state}, &updateResp)
	if updateResp.Diagnostics.HasError() {
		t.Fatalf("expected the null plan value to drain cleanly, got %v", updateResp.Diagnostics.Errors())
	}
	if !putCalled.Load() {
		t.Fatal("expected the PUT to fire for the drain")
	}
	if putBody.ParameterGroupID == nil {
		t.Fatal("expected the PUT to carry the clearing parameterGroupId")
	}
	if *putBody.ParameterGroupID != "" {
		t.Errorf("expected the clearing value to be the empty string, got %q", *putBody.ParameterGroupID)
	}
}
