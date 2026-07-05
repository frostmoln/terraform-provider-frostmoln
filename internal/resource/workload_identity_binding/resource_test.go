package workload_identity_binding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- Model unit tests ---

func TestModelToCreateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	scopes, d := types.ListValueFrom(ctx, types.StringType, []string{"compute:read", "storage:read"})
	diags.Append(d...)

	m := WorkloadIdentityBindingModel{
		ClusterID:      types.StringValue("cl-1"),
		Namespace:      types.StringValue("default"),
		ServiceAccount: types.StringValue("app"),
		Scopes:         scopes,
	}
	req := m.toCreateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags.Errors())
	}
	if req.ClusterID != "cl-1" || req.Namespace != "default" || req.ServiceAccount != "app" {
		t.Errorf("unexpected create request: %+v", req)
	}
	if len(req.Scopes) != 2 || req.Scopes[0] != "compute:read" {
		t.Errorf("unexpected scopes: %v", req.Scopes)
	}
}

func TestModelToUpdateRequest(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	scopes, d := types.ListValueFrom(ctx, types.StringType, []string{"compute:read"})
	diags.Append(d...)

	m := WorkloadIdentityBindingModel{Scopes: scopes}
	req := m.toUpdateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags.Errors())
	}
	if len(req.Scopes) != 1 || req.Scopes[0] != "compute:read" {
		t.Errorf("unexpected update scopes: %v", req.Scopes)
	}
}

func TestModelFromAPI(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	b := &apiWorkloadIdentityBinding{
		ID:             "b-1",
		ClusterID:      "cl-1",
		TenantID:       "tenant-456",
		Namespace:      "default",
		ServiceAccount: "app",
		Scopes:         []string{"compute:read"},
		CreatedAt:      "2026-07-01T12:00:00Z",
		UpdatedAt:      "2026-07-02T12:00:00Z",
	}
	m := WorkloadIdentityBindingModel{Scopes: types.ListNull(types.StringType)}
	m.fromAPI(ctx, b, &diags)
	if diags.HasError() {
		t.Fatalf("diags: %v", diags.Errors())
	}
	if m.ID.ValueString() != "b-1" || m.TenantID.ValueString() != "tenant-456" {
		t.Errorf("unexpected model: %+v", m)
	}
	var scopes []string
	m.Scopes.ElementsAs(ctx, &scopes, false)
	if len(scopes) != 1 || scopes[0] != "compute:read" {
		t.Errorf("unexpected scopes: %v", scopes)
	}
	if m.UpdatedAt.ValueString() != "2026-07-02T12:00:00Z" {
		t.Errorf("unexpected updatedAt: %s", m.UpdatedAt.ValueString())
	}
}

// --- tfsdk-level resource tests ---

func bindingSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &workloadIdentityBindingResource{}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func bindingTFType(t *testing.T) tftypes.Type {
	t.Helper()
	return bindingSchema(t).Type().TerraformType(context.Background())
}

func configuredResource(t *testing.T, serverURL string) *workloadIdentityBindingResource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &workloadIdentityBindingResource{client: c}
}

func meServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
			return
		}
		handler(w, r)
	}))
}

// bindingValue builds a full tftypes value for the schema.
func bindingValue(tfType tftypes.Type, id, cluster, ns, sa string, scopes []string) tftypes.Value {
	scopeVals := make([]tftypes.Value, len(scopes))
	for i, s := range scopes {
		scopeVals[i] = tftypes.NewValue(tftypes.String, s)
	}
	idVal := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	if id != "" {
		idVal = tftypes.NewValue(tftypes.String, id)
	}
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              idVal,
		"cluster_id":      tftypes.NewValue(tftypes.String, cluster),
		"namespace":       tftypes.NewValue(tftypes.String, ns),
		"service_account": tftypes.NewValue(tftypes.String, sa),
		"scopes":          tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, scopeVals),
		"tenant_id":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})
}

func TestResourceMetadata(t *testing.T) {
	r := &workloadIdentityBindingResource{}
	resp := resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_workload_identity_binding" {
		t.Errorf("unexpected type name: %s", resp.TypeName)
	}
}

func TestResourceCreate(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workload-identity/bindings" {
			var req apiCreateWorkloadIdentityBindingRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if req.ClusterID != "cl-1" || req.ServiceAccount != "app" {
				t.Errorf("unexpected create body: %+v", req)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiWorkloadIdentityBinding{
				ID: "b-9", ClusterID: req.ClusterID, TenantID: "tenant-456",
				Namespace: req.Namespace, ServiceAccount: req.ServiceAccount,
				Scopes: req.Scopes, CreatedAt: "t0", UpdatedAt: "t0",
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := bindingSchema(t)
	tfType := bindingTFType(t)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: bindingValue(tfType, "", "cl-1", "default", "app", []string{"compute:read"})}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	res.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diags: %v", resp.Diagnostics.Errors())
	}
	var m WorkloadIdentityBindingModel
	resp.State.Get(ctx, &m)
	if m.ID.ValueString() != "b-9" || m.TenantID.ValueString() != "tenant-456" {
		t.Errorf("unexpected state: %+v", m)
	}
}

// TestResourceCreatePreservesPlanScopeOrder guards the M1 fix: if the server
// echoes scopes in a different order than submitted, the Required scopes attr
// keeps the plan's order so Terraform doesn't error "inconsistent result".
func TestResourceCreatePreservesPlanScopeOrder(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workload-identity/bindings" {
			var req apiCreateWorkloadIdentityBindingRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(http.StatusCreated)
			// Deliberately return the scopes REVERSED.
			_ = json.NewEncoder(w).Encode(apiWorkloadIdentityBinding{
				ID: "b-9", ClusterID: req.ClusterID, TenantID: "tenant-456",
				Namespace: req.Namespace, ServiceAccount: req.ServiceAccount,
				Scopes: []string{"storage:read", "compute:read"}, CreatedAt: "t0", UpdatedAt: "t0",
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := bindingSchema(t)
	tfType := bindingTFType(t)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: bindingValue(tfType, "", "cl-1", "default", "app", []string{"compute:read", "storage:read"})}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	res.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diags: %v", resp.Diagnostics.Errors())
	}
	var m WorkloadIdentityBindingModel
	resp.State.Get(ctx, &m)
	var scopes []string
	m.Scopes.ElementsAs(ctx, &scopes, false)
	if len(scopes) != 2 || scopes[0] != "compute:read" || scopes[1] != "storage:read" {
		t.Errorf("expected plan scope order preserved [compute:read storage:read], got %v", scopes)
	}
}

func TestResourceReadNotFound(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "gone"}})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := bindingSchema(t)
	tfType := bindingTFType(t)

	req := resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: bindingValue(tfType, "b-x", "cl-1", "default", "app", []string{"compute:read"})}}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s}}
	res.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read diags: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after not-found read")
	}
}

// TestResourceUpdatePUT verifies scope changes go out as a PUT and the returned
// binding refreshes state without a second GET.
func TestResourceUpdatePUT(t *testing.T) {
	putCalled := false
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/workload-identity/bindings/b-9" {
			putCalled = true
			var req apiUpdateWorkloadIdentityBindingRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(req.Scopes) != 2 {
				t.Errorf("expected 2 scopes in PUT, got %v", req.Scopes)
			}
			_ = json.NewEncoder(w).Encode(apiWorkloadIdentityBinding{
				ID: "b-9", ClusterID: "cl-1", TenantID: "tenant-456",
				Namespace: "default", ServiceAccount: "app",
				Scopes: req.Scopes, CreatedAt: "t0", UpdatedAt: "t1",
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := bindingSchema(t)
	tfType := bindingTFType(t)

	state := bindingValue(tfType, "b-9", "cl-1", "default", "app", []string{"compute:read"})
	plan := bindingValue(tfType, "b-9", "cl-1", "default", "app", []string{"compute:read", "storage:read"})
	req := resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}, State: tfsdk.State{Schema: s, Raw: state}}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	res.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update diags: %v", resp.Diagnostics.Errors())
	}
	if !putCalled {
		t.Error("expected PUT to be called")
	}
	var m WorkloadIdentityBindingModel
	resp.State.Get(ctx, &m)
	if m.UpdatedAt.ValueString() != "t1" {
		t.Errorf("expected refreshed updatedAt t1, got %s", m.UpdatedAt.ValueString())
	}
	var scopes []string
	m.Scopes.ElementsAs(ctx, &scopes, false)
	if len(scopes) != 2 {
		t.Errorf("expected 2 scopes after update, got %v", scopes)
	}
}

func TestResourceDeleteAlreadyGone(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "NOT_FOUND", "message": "gone"}})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := bindingSchema(t)
	tfType := bindingTFType(t)

	req := resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: bindingValue(tfType, "gone", "cl-1", "default", "app", []string{"compute:read"})}}
	resp := &resource.DeleteResponse{}
	res.Delete(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete of gone binding should be silent, got %v", resp.Diagnostics.Errors())
	}
}

func TestResourceImportState(t *testing.T) {
	r := &workloadIdentityBindingResource{}
	s := bindingSchema(t)
	tfType := bindingTFType(t)
	initVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, nil),
		"cluster_id":      tftypes.NewValue(tftypes.String, nil),
		"namespace":       tftypes.NewValue(tftypes.String, nil),
		"service_account": tftypes.NewValue(tftypes.String, nil),
		"scopes":          tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"tenant_id":       tftypes.NewValue(tftypes.String, nil),
		"created_at":      tftypes.NewValue(tftypes.String, nil),
		"updated_at":      tftypes.NewValue(tftypes.String, nil),
	})
	resp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: initVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "b-123"}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("import diags: %v", resp.Diagnostics.Errors())
	}
	var m WorkloadIdentityBindingModel
	resp.State.Get(context.Background(), &m)
	if m.ID.ValueString() != "b-123" {
		t.Errorf("expected imported id b-123, got %s", m.ID.ValueString())
	}
}
