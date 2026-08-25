package workload_identity_binding

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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

// TestModelScopesOptional covers the ADR-0102 policy-granted shape: a NULL
// scopes list is a legitimate value, not a diagnostic, and the field is omitted
// from the request body on both create and update.
func TestModelScopesOptional(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	m := WorkloadIdentityBindingModel{
		ClusterID:      types.StringValue("cl-1"),
		Namespace:      types.StringValue("default"),
		ServiceAccount: types.StringValue("app"),
		Scopes:         types.ListNull(types.StringType),
	}
	createReq := m.toCreateRequest(ctx, &diags)
	updateReq := m.toUpdateRequest(ctx, &diags)
	if diags.HasError() {
		t.Fatalf("a null scopes list must not produce diagnostics: %v", diags.Errors())
	}
	if len(createReq.Scopes) != 0 || len(updateReq.Scopes) != 0 {
		t.Errorf("scopes = create %v / update %v, want none", createReq.Scopes, updateReq.Scopes)
	}
	// The published schema declares `scopes` as a plain array with no
	// `nullable: true`, so the spec-conformant spelling for "none" is an OMITTED
	// key, not an explicit null. Both bodies must therefore drop the field.
	for name, req := range map[string]any{"create": createReq, "update": updateReq} {
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		if bytes.Contains(body, []byte(`"scopes"`)) {
			t.Errorf("%s body = %s, want the scopes field omitted", name, body)
		}
	}
}

// TestModelScopesUnknownRefused: an unknown list is unreachable today (scopes is
// Optional and not Computed), but it must never be folded into "no scopes" —
// that would silently clear a live workload's grant if the attribute ever gained
// Computed.
func TestModelScopesUnknownRefused(t *testing.T) {
	ctx := context.Background()
	diags := diag.Diagnostics{}
	m := WorkloadIdentityBindingModel{Scopes: types.ListUnknown(types.StringType)}
	m.toUpdateRequest(ctx, &diags)
	if !diags.HasError() {
		t.Error("an unknown scopes list must raise a diagnostic, not silently send no scopes")
	}
}

// TestModelFromAPINormalizesEmptyScopes pins the state shape for a scope-less
// binding to NULL whichever empty JSON the server sends. An empty list in state
// against a null config is a diff Terraform can never converge.
func TestModelFromAPINormalizesEmptyScopes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		scopes []string
	}{
		{"json null", nil},
		{"empty array", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := diag.Diagnostics{}
			m := WorkloadIdentityBindingModel{}
			m.fromAPI(ctx, &apiWorkloadIdentityBinding{ID: "b-1", Scopes: tc.scopes}, &diags)
			if diags.HasError() {
				t.Fatalf("diags: %v", diags.Errors())
			}
			if !m.Scopes.IsNull() {
				t.Errorf("scopes = %v, want a null list", m.Scopes)
			}
		})
	}
}

// --- tfsdk-level resource tests ---

// TestSchemaScopesOptional guards the BK2 contract at the schema level: scopes
// must be Optional (so a policy-granted binding is authorable at all) while
// still rejecting `scopes = []`, which would otherwise be a second,
// un-normalizable spelling of "no scopes".
func TestSchemaScopesOptional(t *testing.T) {
	attr, ok := bindingSchema(t).Attributes["scopes"].(schema.ListAttribute)
	if !ok {
		t.Fatalf("scopes is not a ListAttribute: %T", bindingSchema(t).Attributes["scopes"])
	}
	if attr.Required {
		t.Error("scopes must not be Required — a policy-granted binding carries none (ADR-0102)")
	}
	if !attr.Optional {
		t.Error("scopes must be Optional")
	}

	empty, d := types.ListValueFrom(context.Background(), types.StringType, []string{})
	if d.HasError() {
		t.Fatalf("diags: %v", d.Errors())
	}

	runValidators := func(v types.List) validator.ListResponse {
		resp := validator.ListResponse{}
		for _, val := range attr.Validators {
			val.ValidateList(context.Background(), validator.ListRequest{
				Path:        path.Root("scopes"),
				ConfigValue: v,
			}, &resp)
		}
		return resp
	}

	if resp := runValidators(empty); !resp.Diagnostics.HasError() {
		t.Error("scopes = [] must be rejected; omit the attribute instead")
	}
	// The whole policy-granted shape rides on the validators SKIPPING a null
	// config. If one ever fires on it, every scope-less binding fails at
	// `terraform validate` — and nothing else in this suite would notice.
	if resp := runValidators(types.ListNull(types.StringType)); resp.Diagnostics.HasError() {
		t.Errorf("an omitted scopes attribute must validate cleanly: %v", resp.Diagnostics.Errors())
	}
}

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

// bindingValue builds a full tftypes value for the schema. A nil scopes slice
// yields a NULL list — the config spelling for a policy-granted binding.
func bindingValue(tfType tftypes.Type, id, cluster, ns, sa string, scopes []string) tftypes.Value {
	scopeListType := tftypes.List{ElementType: tftypes.String}
	scopesVal := tftypes.NewValue(scopeListType, nil)
	if scopes != nil {
		scopeVals := make([]tftypes.Value, len(scopes))
		for i, s := range scopes {
			scopeVals[i] = tftypes.NewValue(tftypes.String, s)
		}
		scopesVal = tftypes.NewValue(scopeListType, scopeVals)
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
		"scopes":          scopesVal,
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

// TestResourceCreatePolicyGranted is the BK2 end-to-end case: a binding created
// with no scopes at all. The POST must carry a null scopes field (not an
// omitted-but-required one), and the state must come back with a NULL list even
// though the server echoes `[]` — otherwise the very next plan shows a diff the
// practitioner cannot write away.
func TestResourceCreatePolicyGranted(t *testing.T) {
	var gotBody []byte
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workload-identity/bindings" {
			gotBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			// Echo an EMPTY ARRAY — the shape a Go service that never returns
			// nil slices actually emits.
			_ = json.NewEncoder(w).Encode(apiWorkloadIdentityBinding{
				ID: "b-pg", ClusterID: "cl-1", TenantID: "tenant-456",
				Namespace: "default", ServiceAccount: "app",
				Scopes: []string{}, CreatedAt: "t0", UpdatedAt: "t0",
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

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: bindingValue(tfType, "", "cl-1", "default", "app", nil)}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	res.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create diags: %v", resp.Diagnostics.Errors())
	}
	if bytes.Contains(gotBody, []byte(`"scopes"`)) {
		t.Errorf("create body = %s, want the scopes field omitted", gotBody)
	}
	var m WorkloadIdentityBindingModel
	resp.State.Get(ctx, &m)
	if m.ID.ValueString() != "b-pg" {
		t.Errorf("unexpected state: %+v", m)
	}
	if !m.Scopes.IsNull() {
		t.Errorf("scopes = %v, want a null list after a scope-less create", m.Scopes)
	}
}

// TestResourceUpdateDropsScopes covers removing `scopes` from the config once a
// policy is attached: the PUT must send null (a full replacement that empties
// the flat grant), and state must settle on a null list.
func TestResourceUpdateDropsScopes(t *testing.T) {
	var gotBody []byte
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/workload-identity/bindings/b-9" {
			gotBody, _ = io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(apiWorkloadIdentityBinding{
				ID: "b-9", ClusterID: "cl-1", TenantID: "tenant-456",
				Namespace: "default", ServiceAccount: "app",
				Scopes: []string{}, CreatedAt: "t0", UpdatedAt: "t1",
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
	plan := bindingValue(tfType, "b-9", "cl-1", "default", "app", nil)
	req := resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}, State: tfsdk.State{Schema: s, Raw: state}}
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	res.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update diags: %v", resp.Diagnostics.Errors())
	}
	if bytes.Contains(gotBody, []byte(`"scopes"`)) {
		t.Errorf("PUT body = %s, want the scopes field omitted (a full replacement with the empty set)", gotBody)
	}
	var m WorkloadIdentityBindingModel
	resp.State.Get(ctx, &m)
	if !m.Scopes.IsNull() {
		t.Errorf("scopes = %v, want a null list after dropping scopes", m.Scopes)
	}
}

// TestResourceUpdateLastGrantRejected pins the client half of the ADR-0102
// invariant: emptying a binding's LAST grant source is the SERVER's refusal, and
// the provider must surface it as an error rather than writing a deny-all
// binding into state.
func TestResourceUpdateLastGrantRejected(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code":    "BINDING_WOULD_HAVE_NO_GRANT",
			"message": "the binding would be left with no grant source",
		}})
	})
	defer server.Close()

	ctx := context.Background()
	res := configuredResource(t, server.URL)
	s := bindingSchema(t)
	tfType := bindingTFType(t)

	state := bindingValue(tfType, "b-9", "cl-1", "default", "app", []string{"compute:read"})
	plan := bindingValue(tfType, "b-9", "cl-1", "default", "app", nil)
	req := resource.UpdateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}, State: tfsdk.State{Schema: s, Raw: state}}
	// The framework seeds the response state with the PRIOR state; a failed
	// update must leave it exactly there.
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: state}}
	res.Update(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the server rejects the last-grant removal")
	}
	var m WorkloadIdentityBindingModel
	resp.State.Get(ctx, &m)
	if m.Scopes.IsNull() {
		t.Error("a rejected last-grant removal must leave the prior scopes in state")
	}
}

// TestModifyPlanWarnsOnScopeDrop: emptying a live binding's scopes narrows it to
// whatever its policies allow, and the API only refuses the case where NOTHING
// survives. The plan is the last place a practitioner can be told, so the
// warning has to fire on the narrowing case too — and must NOT fire on create,
// destroy, or an ordinary scope edit.
func TestModifyPlanWarnsOnScopeDrop(t *testing.T) {
	ctx := context.Background()
	res := &workloadIdentityBindingResource{}
	s := bindingSchema(t)
	tfType := bindingTFType(t)

	scoped := bindingValue(tfType, "b-9", "cl-1", "default", "app", []string{"compute:read"})
	widened := bindingValue(tfType, "b-9", "cl-1", "default", "app", []string{"compute:read", "storage:read"})
	dropped := bindingValue(tfType, "b-9", "cl-1", "default", "app", nil)
	nullRaw := tftypes.NewValue(tfType, nil)

	for _, tc := range []struct {
		name      string
		state     tftypes.Value
		plan      tftypes.Value
		wantWarns int
	}{
		{"drops scopes", scoped, dropped, 1},
		{"create", nullRaw, dropped, 0},
		{"destroy", scoped, nullRaw, 0},
		{"edits scopes", scoped, widened, 0},
		{"already scope-less", dropped, dropped, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: s, Raw: tc.plan}}
			res.ModifyPlan(ctx, resource.ModifyPlanRequest{
				State: tfsdk.State{Schema: s, Raw: tc.state},
				Plan:  tfsdk.Plan{Schema: s, Raw: tc.plan},
			}, resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("ModifyPlan errored: %v", resp.Diagnostics.Errors())
			}
			if got := len(resp.Diagnostics.Warnings()); got != tc.wantWarns {
				t.Errorf("warnings = %d, want %d (%v)", got, tc.wantWarns, resp.Diagnostics.Warnings())
			}
		})
	}
}

func TestResourceReadNotFound(t *testing.T) {
	server := meServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
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
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
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
