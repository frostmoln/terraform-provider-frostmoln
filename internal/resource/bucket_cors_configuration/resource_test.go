package bucket_cors_configuration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// --- helpers ---

func corsSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func ruleObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":              tftypes.String,
			"allowed_origins": tftypes.Set{ElementType: tftypes.String},
			"allowed_methods": tftypes.Set{ElementType: tftypes.String},
			"allowed_headers": tftypes.Set{ElementType: tftypes.String},
			"expose_headers":  tftypes.Set{ElementType: tftypes.String},
			"max_age_seconds": tftypes.Number,
		},
	}
}

func corsObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"bucket": tftypes.String,
			"rules":  tftypes.List{ElementType: ruleObjectType()},
		},
	}
}

func stringSet(values ...string) tftypes.Value {
	elems := make([]tftypes.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, tftypes.NewValue(tftypes.String, v))
	}
	if len(elems) == 0 {
		return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil)
	}
	return tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
}

// corsValue builds a whole-resource value with a single rule.
func corsValue(bucket string, rule map[string]tftypes.Value) tftypes.Value {
	return tftypes.NewValue(corsObjectType(), map[string]tftypes.Value{
		"bucket": tftypes.NewValue(tftypes.String, bucket),
		"rules": tftypes.NewValue(tftypes.List{ElementType: ruleObjectType()}, []tftypes.Value{
			tftypes.NewValue(ruleObjectType(), rule),
		}),
	})
}

func minimalRule() map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, nil),
		"allowed_origins": stringSet("https://app.example.com"),
		"allowed_methods": stringSet("GET"),
		"allowed_headers": stringSet(),
		"expose_headers":  stringSet(),
		"max_age_seconds": tftypes.NewValue(tftypes.Number, nil),
	}
}

func configuredResource(t *testing.T, serverURL string) resource.Resource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(
		context.Background(),
		resource.ConfigureRequest{ProviderData: c},
		&resource.ConfigureResponse{},
	)
	return r
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
	})
}

// --- model conversion ---

func TestCORSModelToAPI(t *testing.T) {
	ctx := context.Background()
	origins, _ := types.SetValueFrom(ctx, types.StringType, []string{"https://a.example.com"})
	methods, _ := types.SetValueFrom(ctx, types.StringType, []string{"GET", "PUT"})

	m := BucketCORSConfigurationModel{
		Bucket: types.StringValue("b1"),
		Rules: []corsRuleModel{{
			ID:             types.StringValue("r1"),
			AllowedOrigins: origins,
			AllowedMethods: methods,
			AllowedHeaders: types.SetNull(types.StringType),
			ExposeHeaders:  types.SetNull(types.StringType),
			MaxAgeSeconds:  types.Int64Value(3600),
		}},
	}

	out, diags := m.toAPI(ctx)
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if len(out.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(out.Rules))
	}
	got := out.Rules[0]
	if got.ID != "r1" {
		t.Errorf("expected id r1, got %q", got.ID)
	}
	if got.MaxAgeSeconds != 3600 {
		t.Errorf("expected maxAgeSeconds 3600, got %d", got.MaxAgeSeconds)
	}
	if len(got.AllowedOrigins) != 1 || got.AllowedOrigins[0] != "https://a.example.com" {
		t.Errorf("unexpected allowedOrigins %v", got.AllowedOrigins)
	}
	if len(got.AllowedMethods) != 2 {
		t.Errorf("expected 2 allowedMethods, got %v", got.AllowedMethods)
	}
	// A null optional set must not become an empty list: omitempty then drops
	// the key, which is what keeps the API's absent/zero equivalence intact.
	if got.AllowedHeaders != nil {
		t.Errorf("expected nil allowedHeaders for a null set, got %v", got.AllowedHeaders)
	}
}

// TestCORSModelFromAPIZeroValuesBecomeNull is the round-trip guard. Every
// optional field carries omitempty on the wire, so an omitted field arrives as
// a zero value. Mapping those to "" / 0 / [] instead of null would differ from
// the practitioner's null config and plan a change on every run.
func TestCORSModelFromAPIZeroValuesBecomeNull(t *testing.T) {
	ctx := context.Background()
	var m BucketCORSConfigurationModel

	diags := m.fromAPI(ctx, []apiCORSRule{{
		AllowedOrigins: []string{"https://a.example.com"},
		AllowedMethods: []string{"GET"},
	}})
	if diags.HasError() {
		t.Fatalf("unexpected diags: %v", diags)
	}
	if len(m.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(m.Rules))
	}

	got := m.Rules[0]
	if !got.ID.IsNull() {
		t.Errorf("expected null id, got %v", got.ID)
	}
	if !got.MaxAgeSeconds.IsNull() {
		t.Errorf("expected null max_age_seconds, got %v", got.MaxAgeSeconds)
	}
	if !got.AllowedHeaders.IsNull() {
		t.Errorf("expected null allowed_headers, got %v", got.AllowedHeaders)
	}
	if !got.ExposeHeaders.IsNull() {
		t.Errorf("expected null expose_headers, got %v", got.ExposeHeaders)
	}
	if got.AllowedOrigins.IsNull() {
		t.Error("expected allowed_origins to be populated")
	}
}

// --- resource plumbing ---

func TestCORSNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestCORSMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_bucket_cors_configuration" {
		t.Errorf("unexpected type name %s", resp.TypeName)
	}
}

func TestCORSSchema(t *testing.T) {
	s := corsSchema(t)
	for _, name := range []string{"bucket", "rules"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("expected attribute %s in schema", name)
		}
	}
}

func TestCORSConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestCORSConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: true}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- CRUD ---

func TestCORSResourceCreate(t *testing.T) {
	var gotBody apiCORSRules
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-123/buckets/b1/cors" {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: corsValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	// The service wraps the list in a "rules" object on PUT; a bare array is a
	// 400 from its binding.
	if len(gotBody.Rules) != 1 {
		t.Fatalf("expected the server to receive 1 wrapped rule, got %+v", gotBody)
	}
	if len(gotBody.Rules[0].AllowedMethods) != 1 || gotBody.Rules[0].AllowedMethods[0] != "GET" {
		t.Errorf("unexpected allowedMethods %v", gotBody.Rules[0].AllowedMethods)
	}

	var state BucketCORSConfigurationModel
	resp.State.Get(context.Background(), &state)
	if state.Bucket.ValueString() != "b1" {
		t.Errorf("expected bucket b1, got %s", state.Bucket.ValueString())
	}
}

func TestCORSResourceRead(t *testing.T) {
	// Literal JSON, not an encoded apiCORSRules: encoding the response through
	// the same struct the code decodes with makes a wrong `json:` tag
	// round-trip through its own mistake and the test stay green. These are the
	// field names storage actually emits (storage/internal/domain/bucket.go).
	const body = `{"rules":[{
		"id":"r1",
		"allowedOrigins":["https://app.example.com"],
		"allowedMethods":["GET"],
		"allowedHeaders":["*"],
		"exposeHeaders":["ETag"],
		"maxAgeSeconds":60
	}]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1/cors" {
			_, _ = w.Write([]byte(body))
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state BucketCORSConfigurationModel
	resp.State.Get(context.Background(), &state)
	if len(state.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(state.Rules))
	}
	if state.Rules[0].ID.ValueString() != "r1" {
		t.Errorf("expected id r1, got %v", state.Rules[0].ID)
	}
	if state.Rules[0].MaxAgeSeconds.ValueInt64() != 60 {
		t.Errorf("expected max_age_seconds 60, got %v", state.Rules[0].MaxAgeSeconds)
	}
	if state.Rules[0].AllowedOrigins.IsNull() || state.Rules[0].ExposeHeaders.IsNull() {
		t.Error("expected the origin and expose-header sets to decode from the wire field names")
	}
}

// TestCORSRulesRejectsEmptyList pins the validator, not its presence. `rules`
// being Required only means non-null: `rules = []` marshals to {"rules":[]},
// which the API answers 204 by DELETING the bucket's whole CORS configuration —
// from a config that reads like it is adding rules.
func TestCORSRulesRejectsEmptyList(t *testing.T) {
	s := corsSchema(t)
	rulesAttr, ok := s.Attributes["rules"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("expected rules to be a ListNestedAttribute, got %T", s.Attributes["rules"])
	}
	if len(rulesAttr.Validators) == 0 {
		t.Fatal("rules has no validators")
	}

	empty, diags := types.ListValue(rulesAttr.NestedObject.Type(), []attr.Value{})
	if diags.HasError() {
		t.Fatalf("building an empty list: %v", diags)
	}

	resp := &validator.ListResponse{}
	for _, v := range rulesAttr.Validators {
		v.ValidateList(context.Background(),
			validator.ListRequest{Path: path.Root("rules"), ConfigValue: empty}, resp)
	}
	if !resp.Diagnostics.HasError() {
		t.Error("expected an empty rules list to be rejected at plan time")
	}
}

// TestCORSReadNotFoundWithLiveBucketErrors covers the 404 that does NOT mean
// "deleted": the service answers 404 when a tenant's object-storage account
// cannot be resolved, and silently forgetting the resource on a backend failure
// makes the next apply blind-write the stored rules over the live bucket.
func TestCORSReadNotFoundWithLiveBucketErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The bucket is alive...
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1" {
			_, _ = w.Write([]byte(`{"name":"b1"}`))
			return
		}
		// ...but its CORS sub-resource 404s.
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected an error when the sub-resource 404s but the bucket is still present")
	}
	if resp.State.Raw.IsNull() {
		t.Error("state must be kept, not silently dropped, when the bucket still exists")
	}
}

func TestCORSDeleteNotFoundWithLiveBucketErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1" {
			_, _ = w.Write([]byte(`{"name":"b1"}`))
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("a destroy must not report success when the bucket is still present and the delete 404'd")
	}
}

// TestCORSModifyPlanWarnsAboutRemovedOrigins is the plan-time control that the
// schema description cannot be: nobody reads a registry page during a CI apply.
func TestCORSModifyPlanWarnsAboutRemovedOrigins(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1/cors" {
			_, _ = w.Write([]byte(`{"rules":[{"allowedOrigins":["https://portal.example.test"],"allowedMethods":["GET"]}]}`))
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.ModifyPlanResponse{}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: corsValue("b1", minimalRule())},
	}, resp)

	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("expected a warning naming the origin this apply stops allowing")
	}
	if !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "https://portal.example.test") {
		t.Errorf("the warning must name the origin being removed, got %q", resp.Diagnostics.Warnings()[0].Detail())
	}
}

// TestCORSModifyPlanWarnsOnWildcardWithWrites: "*" plus a write method lets any
// page on the internet drive that request from a visitor's browser.
func TestCORSModifyPlanWarnsOnWildcardWithWrites(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1/cors" {
			_, _ = w.Write([]byte(`{"rules":[]}`))
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	wildcard := minimalRule()
	wildcard["allowed_origins"] = stringSet("*")
	wildcard["allowed_methods"] = stringSet("GET", "PUT")

	resp := &resource.ModifyPlanResponse{}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: corsValue("b1", wildcard)},
	}, resp)

	found := false
	for _, w := range resp.Diagnostics.Warnings() {
		if strings.Contains(w.Summary(), "any origin to make write requests") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a wildcard-with-writes warning, got %v", resp.Diagnostics.Warnings())
	}
}

// TestCORSModifyPlanSurvivesReadFailure — a plan must never fail because the
// advisory lookup could not reach the API.
func TestCORSModifyPlanSurvivesReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.ModifyPlanResponse{}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: corsValue("b1", minimalRule())},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("a failed advisory read must not fail the plan, got %v", resp.Diagnostics)
	}
}

// TestCORSResourceReadEmptyRemovesState covers the drift path that a 404 does
// not: a bucket whose CORS configuration was deleted outside Terraform answers
// 200 with an empty rule set, not 404. Keeping it in state would plan an update
// against a configuration that no longer exists.
func TestCORSResourceReadEmptyRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1/cors" {
			_ = json.NewEncoder(w).Encode(apiCORSRules{Rules: []apiCORSRule{}})
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state when the API returns no rules")
	}
}

func TestCORSResourceReadNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: corsValue("gone", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: corsValue("gone", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state on 404")
	}
}

func TestCORSResourceUpdate(t *testing.T) {
	var gotBody apiCORSRules
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-123/buckets/b1/cors" {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	updated := minimalRule()
	updated["allowed_methods"] = stringSet("GET", "PUT")

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{
		State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())},
		Plan:  tfsdk.Plan{Schema: s, Raw: corsValue("b1", updated)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if len(gotBody.Rules) != 1 || len(gotBody.Rules[0].AllowedMethods) != 2 {
		t.Errorf("expected the updated methods to reach the API, got %+v", gotBody)
	}
}

func TestCORSResourceDelete(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/buckets/b1/cors" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: corsValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !called {
		t.Error("expected DELETE to reach the CORS sub-resource")
	}
}

func TestCORSResourceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := corsSchema(t)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: corsValue("gone", minimalRule())}}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: corsValue("gone", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("a already-deleted configuration must not error, got %v", resp.Diagnostics)
	}
}
