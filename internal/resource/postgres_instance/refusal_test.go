package postgres_instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	resSchema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// Class A (2026-09 convergence audit): parameter_group_id is stored and
// echoed by the platform and never applied — the database service's
// parameter-apply endpoint is an unimplemented stub. The plan-time validator
// (internal/unenacted) refuses every known configuration value; these tests
// drive the Create/Update belts that stop an apply-resolved value from
// reaching the API, and assert the refusal names the real constraint.

// instanceWithParameterGroup builds a model whose storage/flavor match
// across plan and state, so the belt is the only thing that can fire.
func instanceWithParameterGroup(t *testing.T, parameterGroupValue types.String) PostgresInstanceModel {
	t.Helper()
	return PostgresInstanceModel{
		ID:               types.StringValue("pg-123"),
		Name:             types.StringValue("my-pg"),
		Version:          types.StringValue("16"),
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

	r := newResource(newClient(t, server))
	plan := buildPlan(t, instanceWithParameterGroup(t, types.StringValue("pg-1")))

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), createRequest(plan), &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected the create carrying parameter_group_id to be refused")
	}
	diag := createResp.Diagnostics.Errors()[0]
	if got, want := diag.Summary(), parameterGroupRefusalTitle; got != want {
		t.Errorf("expected summary %q, got %q", want, got)
	}
	for _, want := range []string{"stored, never applied", "parameter-group APIs"} {
		if !strings.Contains(diag.Detail(), want) {
			t.Errorf("expected the diagnostic to name %q, got %q", want, diag.Detail())
		}
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

	r := newResource(newClient(t, server))
	plan := buildPlan(t, instanceWithParameterGroup(t, types.StringValue("pg-2")))
	state := buildState(t, instanceWithParameterGroup(t, types.StringValue("pg-1")))

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), updateRequest(plan, state), &updateResp)

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

// TestUpdateDrainsStoredParameterGroup verifies a stored id from an older
// provider drains cleanly: the attribute is omitted from the configuration,
// the plan value is null, the belt does not fire, and the PUT carries the
// clearing value to an API that stores-and-echoes it.
//
// Platform-side verification for the clearing value (pre-review, measured
// against the database service): UpdateInstanceRequest.Validate never
// inspects parameterGroupId, the service assigns the pointer's value onto
// the row, and the repository write maps "" through nullString to SQL NULL —
// which the parameter_group_id UUID FK accepts. The drain cannot wedge on
// the platform validating or rejecting the empty reference.
func TestUpdateDrainsStoredParameterGroup(t *testing.T) {
	var putBody apiUpdatePostgresInstanceRequest
	putCalled := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-1/databases/pg-123":
			putCalled.Store(true)
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Errorf("PUT body decode failed: %v", err)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-1/databases/pg-123":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"pg-123","name":"my-pg","status":"running"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/extensions"):
			// The extension ledger read accompanies every refresh; empty here.
			_ = json.NewEncoder(w).Encode(map[string]any{"extensionRevision": 0, "extensions": []map[string]any{}})
		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := newResource(newClient(t, server))
	plan := buildPlan(t, instanceWithParameterGroup(t, types.StringNull()))
	state := buildState(t, instanceWithParameterGroup(t, types.StringValue("pg-1")))

	updateResp := resource.UpdateResponse{State: state}
	r.Update(context.Background(), updateRequest(plan, state), &updateResp)
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
