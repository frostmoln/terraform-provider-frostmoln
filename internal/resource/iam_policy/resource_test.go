package iam_policy

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

const sampleDoc = `{"schemaVersion":"1","rules":[{"access":"allow","operations":["compute:instances:read"],"targets":["*"]}]}`

// --- model unit tests ---

func TestToCreateRequest(t *testing.T) {
	diags := diag.Diagnostics{}
	m := IAMPolicyModel{
		Name:        types.StringValue("read-only"),
		Description: types.StringValue("compute read"),
		Document:    types.StringValue(sampleDoc),
	}
	req := m.toCreateRequest(&diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags.Errors())
	}
	if req.Name != "read-only" || req.Description != "compute read" {
		t.Errorf("unexpected head: %+v", req)
	}
	if string(req.Document) != sampleDoc {
		t.Errorf("document not passed through: %s", req.Document)
	}
}

func TestToCreateRequestInvalidJSON(t *testing.T) {
	diags := diag.Diagnostics{}
	m := IAMPolicyModel{Name: types.StringValue("x"), Document: types.StringValue("{not json")}
	m.toCreateRequest(&diags)
	if !diags.HasError() {
		t.Fatal("expected a diagnostic for invalid JSON document")
	}
}

func TestToUpdateRequestClearsDescription(t *testing.T) {
	diags := diag.Diagnostics{}
	m := IAMPolicyModel{
		Name:        types.StringValue("x"),
		Description: types.StringNull(), // clearing
		Document:    types.StringValue(sampleDoc),
	}
	req := m.toUpdateRequest(&diags)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags.Errors())
	}
	if req.Description == nil || *req.Description != "" {
		t.Errorf("expected description cleared to empty string, got %v", req.Description)
	}
	if req.Name == nil || *req.Name != "x" {
		t.Errorf("expected name sent, got %v", req.Name)
	}
}

func TestFromAPIPreservesEquivalentDocument(t *testing.T) {
	// Config holds a compact doc; the server returns the same policy with keys
	// reordered and whitespace. State must keep the config string.
	m := IAMPolicyModel{
		Description: types.StringNull(),
		Document:    types.StringValue(sampleDoc),
	}
	reordered := `{
      "rules": [ { "targets": ["*"], "operations": ["compute:instances:read"], "access": "allow" } ],
      "schemaVersion": "1"
    }`
	m.fromAPI(&apiPolicy{
		ID:        "pol-1",
		Name:      "read-only",
		Document:  json.RawMessage(reordered),
		Version:   1,
		CreatedAt: "2026-07-08T00:00:00Z",
		UpdatedAt: "2026-07-08T00:00:00Z",
	})
	if m.Document.ValueString() != sampleDoc {
		t.Errorf("expected config document preserved, got %s", m.Document.ValueString())
	}
	if m.ID.ValueString() != "pol-1" || m.Version.ValueInt64() != 1 {
		t.Errorf("unexpected computed fields: id=%s version=%d", m.ID.ValueString(), m.Version.ValueInt64())
	}
}

func TestFromAPIAdoptsChangedDocument(t *testing.T) {
	m := IAMPolicyModel{
		Description: types.StringNull(),
		Document:    types.StringValue(sampleDoc),
	}
	changed := `{"schemaVersion":"1","rules":[{"access":"deny","operations":["compute:*:delete"],"targets":["*"]}]}`
	m.fromAPI(&apiPolicy{ID: "pol-2", Name: "x", Document: json.RawMessage(changed)})
	if m.Document.ValueString() != changed {
		t.Errorf("expected changed document adopted, got %s", m.Document.ValueString())
	}
}

func TestFromAPIImportAdoptsDocument(t *testing.T) {
	// On import the model document is null -> adopt the API value.
	m := IAMPolicyModel{Description: types.StringNull(), Document: types.StringNull()}
	m.fromAPI(&apiPolicy{ID: "pol-3", Name: "x", Document: json.RawMessage(sampleDoc)})
	if m.Document.ValueString() != sampleDoc {
		t.Errorf("expected API document adopted on import, got %s", m.Document.ValueString())
	}
}

// --- TFSDK CRUD tests ---

func meHandler(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-1", "tenantId": "tenant-1", "email": "t@example.com"})
}

func meServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			meHandler(w, r)
			return
		}
		handler(w, r)
	}))
}

func policySchema(t *testing.T) schema.Schema {
	t.Helper()
	r := &iamPolicyResource{}
	resp := resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func configuredResource(t *testing.T, url string) *iamPolicyResource {
	t.Helper()
	c := client.NewClient(url, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return &iamPolicyResource{client: c}
}

func policyStateValue(tfType tftypes.Type, id, name, doc string) tftypes.Value {
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, id),
		"name":        tftypes.NewValue(tftypes.String, name),
		"description": tftypes.NewValue(tftypes.String, nil),
		"document":    tftypes.NewValue(tftypes.String, doc),
		"tenant_id":   tftypes.NewValue(tftypes.String, "tenant-1"),
		"authored_by": tftypes.NewValue(tftypes.String, "user-1"),
		"version":     tftypes.NewValue(tftypes.Number, 1),
		"created_at":  tftypes.NewValue(tftypes.String, "2026-07-08T00:00:00Z"),
		"updated_at":  tftypes.NewValue(tftypes.String, "2026-07-08T00:00:00Z"),
	})
}

func TestResource_Create(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/iam/policies" {
			var req apiCreatePolicyRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apiPolicy{
				ID: "pol-new", Name: req.Name, Document: req.Document, TenantID: "tenant-1",
				AuthoredBy: "user-1", Version: 1, CreatedAt: "2026-07-08T00:00:00Z", UpdatedAt: "2026-07-08T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := policySchema(t)
	tfType := s.Type().TerraformType(ctx)

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"name":        tftypes.NewValue(tftypes.String, "read-only"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"document":    tftypes.NewValue(tftypes.String, sampleDoc),
		"tenant_id":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"authored_by": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"version":     tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	res.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	var m IAMPolicyModel
	resp.State.Get(ctx, &m)
	if m.ID.ValueString() != "pol-new" {
		t.Errorf("id = %s", m.ID.ValueString())
	}
	if m.Document.ValueString() != sampleDoc {
		t.Errorf("document not preserved: %s", m.Document.ValueString())
	}
	if m.TenantID.ValueString() != "tenant-1" {
		t.Errorf("tenant_id = %s", m.TenantID.ValueString())
	}
}

func TestResource_Read_NotFound(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := policySchema(t)
	tfType := s.Type().TerraformType(ctx)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s}}
	res.Read(ctx, resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: policyStateValue(tfType, "pol-x", "x", sampleDoc)}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected null state after not-found")
	}
}

func TestResource_Update(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/v1/iam/policies/pol-1" {
			var req apiUpdatePolicyRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(apiPolicy{
				ID: "pol-1", Name: *req.Name, Document: req.Document, TenantID: "tenant-1",
				AuthoredBy: "user-1", Version: 2, CreatedAt: "2026-07-08T00:00:00Z", UpdatedAt: "2026-07-08T01:00:00Z",
			})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := policySchema(t)
	tfType := s.Type().TerraformType(ctx)
	newDoc := `{"schemaVersion":"1","rules":[{"access":"deny","operations":["compute:*:delete"],"targets":["*"]}]}`

	planVal := tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":          tftypes.NewValue(tftypes.String, "pol-1"),
		"name":        tftypes.NewValue(tftypes.String, "renamed"),
		"description": tftypes.NewValue(tftypes.String, nil),
		"document":    tftypes.NewValue(tftypes.String, newDoc),
		"tenant_id":   tftypes.NewValue(tftypes.String, "tenant-1"),
		"authored_by": tftypes.NewValue(tftypes.String, "user-1"),
		"version":     tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":  tftypes.NewValue(tftypes.String, "2026-07-08T00:00:00Z"),
		"updated_at":  tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	res.Update(ctx, resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: s, Raw: planVal},
		State: tfsdk.State{Schema: s, Raw: policyStateValue(tfType, "pol-1", "read-only", sampleDoc)},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	var m IAMPolicyModel
	resp.State.Get(ctx, &m)
	if m.Name.ValueString() != "renamed" || m.Version.ValueInt64() != 2 {
		t.Errorf("unexpected update result: name=%s version=%d", m.Name.ValueString(), m.Version.ValueInt64())
	}
	if m.Document.ValueString() != newDoc {
		t.Errorf("document not updated: %s", m.Document.ValueString())
	}
}

func TestResource_Delete(t *testing.T) {
	deleted := false
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/iam/policies/pol-1" {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := policySchema(t)
	tfType := s.Type().TerraformType(ctx)

	resp := &resource.DeleteResponse{}
	res.Delete(ctx, resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: policyStateValue(tfType, "pol-1", "x", sampleDoc)}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics.Errors())
	}
	if !deleted {
		t.Error("expected delete call")
	}
}

func TestResource_Metadata_Import(t *testing.T) {
	r := &iamPolicyResource{}
	mResp := resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mResp)
	if mResp.TypeName != "frostmoln_iam_policy" {
		t.Errorf("type name = %s", mResp.TypeName)
	}

	s := policySchema(t)
	tfType := s.Type().TerraformType(context.Background())
	initVal := policyStateValue(tfType, "", "", "")
	iResp := &resource.ImportStateResponse{State: tfsdk.State{Schema: s, Raw: initVal}}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "pol-imported"}, iResp)
	if iResp.Diagnostics.HasError() {
		t.Fatalf("unexpected import errors: %v", iResp.Diagnostics.Errors())
	}
	var m IAMPolicyModel
	iResp.State.Get(context.Background(), &m)
	if m.ID.ValueString() != "pol-imported" {
		t.Errorf("imported id = %s", m.ID.ValueString())
	}
}
