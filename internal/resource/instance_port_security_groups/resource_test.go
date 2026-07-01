package instance_port_security_groups

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestFindPort(t *testing.T) {
	sg := &apiInstanceSecurityGroups{
		Uniform: false,
		Ports: []apiInstancePortSecurityGroups{
			{PortID: "port-1", SecurityGroupIDs: []string{"sg-1"}},
			{PortID: "port-2", SecurityGroupIDs: []string{"sg-2", "sg-3"}},
		},
	}
	if p := sg.findPort("port-2"); p == nil || len(p.SecurityGroupIDs) != 2 {
		t.Errorf("expected port-2 with 2 SGs, got %+v", p)
	}
	if p := sg.findPort("port-missing"); p != nil {
		t.Errorf("expected nil for missing port, got %+v", p)
	}
}

func newFastResource(t *testing.T, c *client.Client) resource.Resource {
	t.Helper()
	r := NewResource()
	// Inject a short poll interval so the async-operation wait doesn't stall the test.
	r.(*instancePortSecurityGroupsResource).pollInterval = 10 * time.Millisecond
	rc := r.(resource.ResourceWithConfigure)
	var configResp resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}
	return r
}

func getSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	return schemaResp
}

func sgSetValue(ids ...string) tftypes.Value {
	elems := make([]tftypes.Value, 0, len(ids))
	for _, id := range ids {
		elems = append(elems, tftypes.NewValue(tftypes.String, id))
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
}

func TestTFSDKCreate(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "tenant-456"})

		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-1/ports/port-1/security-groups":
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"operationId": "op-1", "status": "accepted", "resourceType": "instance"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/op-1":
			_ = json.NewEncoder(w).Encode(map[string]string{"operationId": "op-1", "status": "completed", "resourceType": "instance"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-1/security-groups":
			_ = json.NewEncoder(w).Encode(apiInstanceSecurityGroups{
				SecurityGroupIDs: []string{"sg-1"},
				Uniform:          false,
				Ports: []apiInstancePortSecurityGroups{
					{PortID: "port-1", SecurityGroupIDs: []string{"sg-1"}},
					{PortID: "port-2", SecurityGroupIDs: []string{"sg-2"}},
				},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "not found"}})
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := newFastResource(t, c)
	schemaResp := getSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"instance_id":     tftypes.NewValue(tftypes.String, "inst-1"),
		"port_id":         tftypes.NewValue(tftypes.String, "port-1"),
		"security_groups": sgSetValue("sg-1"),
	})

	createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal}}
	var createResp resource.CreateResponse
	createResp.State = tfsdk.State{Schema: schemaResp.Schema}
	r.Create(ctx, createReq, &createResp)

	if createResp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", createResp.Diagnostics.Errors())
	}
	// Body carried exactly the configured set (no clear flag).
	if ids, _ := gotBody["securityGroupIds"].([]any); len(ids) != 1 {
		t.Errorf("expected 1 securityGroupId in body, got %v", gotBody["securityGroupIds"])
	}
	if c, _ := gotBody["clearSecurityGroups"].(bool); c {
		t.Error("clearSecurityGroups should be false/omitted for a non-empty set")
	}

	var model InstancePortSecurityGroupsModel
	createResp.State.Get(ctx, &model)
	if model.ID.ValueString() != "inst-1/port-1" {
		t.Errorf("expected ID inst-1/port-1, got %s", model.ID.ValueString())
	}
	var sgs []string
	model.SecurityGroups.ElementsAs(ctx, &sgs, false)
	if len(sgs) != 1 || sgs[0] != "sg-1" {
		t.Errorf("expected security_groups [sg-1], got %v", sgs)
	}
}

func TestTFSDKRead_PortGone_RemovesResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-1/security-groups":
			// port-1 no longer present -> resource should be removed from state.
			_ = json.NewEncoder(w).Encode(apiInstanceSecurityGroups{
				Uniform: true,
				Ports:   []apiInstancePortSecurityGroups{{PortID: "port-2", SecurityGroupIDs: []string{"sg-2"}}},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := newFastResource(t, c)
	schemaResp := getSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "inst-1/port-1"),
		"instance_id":     tftypes.NewValue(tftypes.String, "inst-1"),
		"port_id":         tftypes.NewValue(tftypes.String, "port-1"),
		"security_groups": sgSetValue("sg-1"),
	})

	readReq := resource.ReadRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
	r.Read(ctx, readReq, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}
	if !readResp.State.Raw.IsNull() {
		t.Error("expected state to be removed (null) when the port is gone")
	}
}

func TestTFSDKRead_DriftUpdatesSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u-1", "tenantId": "tenant-456"})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-1/security-groups":
			// Out-of-band change: port-1 now holds sg-9 instead of sg-1.
			_ = json.NewEncoder(w).Encode(apiInstanceSecurityGroups{
				Uniform: false,
				Ports:   []apiInstancePortSecurityGroups{{PortID: "port-1", SecurityGroupIDs: []string{"sg-9"}}},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}

	r := newFastResource(t, c)
	schemaResp := getSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	stateVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "inst-1/port-1"),
		"instance_id":     tftypes.NewValue(tftypes.String, "inst-1"),
		"port_id":         tftypes.NewValue(tftypes.String, "port-1"),
		"security_groups": sgSetValue("sg-1"),
	})

	readReq := resource.ReadRequest{State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}}
	var readResp resource.ReadResponse
	readResp.State = tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal}
	r.Read(ctx, readReq, &readResp)

	if readResp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", readResp.Diagnostics.Errors())
	}
	var model InstancePortSecurityGroupsModel
	readResp.State.Get(ctx, &model)
	var sgs []string
	model.SecurityGroups.ElementsAs(ctx, &sgs, false)
	if len(sgs) != 1 || sgs[0] != "sg-9" {
		t.Errorf("expected drift to surface security_groups [sg-9], got %v", sgs)
	}
}
