package bucket_lifecycle_configuration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func lifecycleSchema(t *testing.T) schema.Schema {
	t.Helper()
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	return resp.Schema
}

func ruleObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":                                     tftypes.String,
			"enabled":                                tftypes.Bool,
			"prefix":                                 tftypes.String,
			"expiration_days":                        tftypes.Number,
			"noncurrent_version_expiration_days":     tftypes.Number,
			"abort_incomplete_multipart_upload_days": tftypes.Number,
		},
	}
}

func lifecycleObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"bucket": tftypes.String,
			"rules":  tftypes.List{ElementType: ruleObjectType()},
		},
	}
}

func lifecycleValue(bucket string, rule map[string]tftypes.Value) tftypes.Value {
	return tftypes.NewValue(lifecycleObjectType(), map[string]tftypes.Value{
		"bucket": tftypes.NewValue(tftypes.String, bucket),
		"rules": tftypes.NewValue(tftypes.List{ElementType: ruleObjectType()}, []tftypes.Value{
			tftypes.NewValue(ruleObjectType(), rule),
		}),
	})
}

func minimalRule() map[string]tftypes.Value {
	return map[string]tftypes.Value{
		"id":                                     tftypes.NewValue(tftypes.String, "expire-old"),
		"enabled":                                tftypes.NewValue(tftypes.Bool, true),
		"prefix":                                 tftypes.NewValue(tftypes.String, nil),
		"expiration_days":                        tftypes.NewValue(tftypes.Number, 30),
		"noncurrent_version_expiration_days":     tftypes.NewValue(tftypes.Number, nil),
		"abort_incomplete_multipart_upload_days": tftypes.NewValue(tftypes.Number, nil),
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

func TestLifecycleModelToAPI(t *testing.T) {
	m := BucketLifecycleConfigurationModel{
		Bucket: types.StringValue("b1"),
		Rules: []lifecycleRuleModel{{
			ID:                                 types.StringValue("r1"),
			Enabled:                            types.BoolValue(true),
			Prefix:                             types.StringValue("logs/"),
			ExpirationDays:                     types.Int64Value(30),
			NoncurrentVersionExpirationDays:    types.Int64Null(),
			AbortIncompleteMultipartUploadDays: types.Int64Value(7),
		}},
	}

	out := m.toAPI()
	if len(out.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(out.Rules))
	}
	got := out.Rules[0]
	if got.ID != "r1" || !got.Enabled || got.Prefix != "logs/" {
		t.Errorf("unexpected rule %+v", got)
	}
	if got.ExpirationDays != 30 || got.AbortIncompleteMultipartUploadDays != 7 {
		t.Errorf("unexpected day counts %+v", got)
	}
	if got.NoncurrentVersionExpirationDays != 0 {
		t.Errorf("a null day count must send the zero value so omitempty drops it, got %d", got.NoncurrentVersionExpirationDays)
	}
}

// TestLifecycleModelFromAPIZeroValuesBecomeNull is the round-trip guard. Every
// optional field carries omitempty on the wire, so an omitted field arrives as
// a zero value. Mapping those to "" / 0 instead of null would differ from the
// practitioner's null config and plan a change on every run.
func TestLifecycleModelFromAPIZeroValuesBecomeNull(t *testing.T) {
	var m BucketLifecycleConfigurationModel
	m.fromAPI([]apiLifecycleRule{{ID: "r1", Enabled: true, ExpirationDays: 30}})

	if len(m.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(m.Rules))
	}
	got := m.Rules[0]
	if !got.Prefix.IsNull() {
		t.Errorf("expected null prefix, got %v", got.Prefix)
	}
	if !got.NoncurrentVersionExpirationDays.IsNull() {
		t.Errorf("expected null noncurrent_version_expiration_days, got %v", got.NoncurrentVersionExpirationDays)
	}
	if !got.AbortIncompleteMultipartUploadDays.IsNull() {
		t.Errorf("expected null abort_incomplete_multipart_upload_days, got %v", got.AbortIncompleteMultipartUploadDays)
	}
	if got.ExpirationDays.ValueInt64() != 30 {
		t.Errorf("expected expiration_days 30, got %v", got.ExpirationDays)
	}
	// enabled=false is a real state, not an absence — it must survive as false
	// rather than collapsing to null the way the optional fields do.
	m.fromAPI([]apiLifecycleRule{{ID: "r1", Enabled: false, ExpirationDays: 1}})
	if m.Rules[0].Enabled.IsNull() || m.Rules[0].Enabled.ValueBool() {
		t.Errorf("expected enabled=false to survive, got %v", m.Rules[0].Enabled)
	}
}

// TestLifecycleSchemaOmitsUnsupportedFields pins the deliberate omission. The
// storage service rejects transitions and per-rule tag filters with 400 because
// the object backend stores neither; offering them here would be a knob that
// cannot work.
func TestLifecycleSchemaOmitsUnsupportedFields(t *testing.T) {
	s := lifecycleSchema(t)
	rules, ok := s.Attributes["rules"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("expected rules to be a ListNestedAttribute, got %T", s.Attributes["rules"])
	}
	for _, name := range []string{"transition_days", "transition_storage_class", "tags"} {
		if _, present := rules.NestedObject.Attributes[name]; present {
			t.Errorf("%s must not be exposed: the API rejects it with 400", name)
		}
	}
}

// --- resource plumbing ---

func TestLifecycleNewResource(t *testing.T) {
	if NewResource() == nil {
		t.Fatal("expected non-nil resource")
	}
}

func TestLifecycleMetadata(t *testing.T) {
	r := NewResource()
	resp := &resource.MetadataResponse{}
	r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_bucket_lifecycle_configuration" {
		t.Errorf("unexpected type name %s", resp.TypeName)
	}
}

func TestLifecycleSchema(t *testing.T) {
	s := lifecycleSchema(t)
	for _, name := range []string{"bucket", "rules"} {
		if _, ok := s.Attributes[name]; !ok {
			t.Errorf("expected attribute %s in schema", name)
		}
	}
}

func TestLifecycleConfigureNilProviderData(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected no errors, got %v", resp.Diagnostics)
	}
}

func TestLifecycleConfigureWrongType(t *testing.T) {
	r := NewResource()
	resp := &resource.ConfigureResponse{}
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: true}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected error for wrong type")
	}
}

// --- CRUD ---

func TestLifecycleResourceCreate(t *testing.T) {
	var gotBody apiLifecycleRules
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-123/buckets/b1/lifecycle" {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	// The service wraps the list in a "rules" object on PUT; a bare array is a
	// 400 from its binding.
	if len(gotBody.Rules) != 1 {
		t.Fatalf("expected the server to receive 1 wrapped rule, got %+v", gotBody)
	}
	if gotBody.Rules[0].ID != "expire-old" || gotBody.Rules[0].ExpirationDays != 30 {
		t.Errorf("unexpected rule reached the API: %+v", gotBody.Rules[0])
	}
}

func TestLifecycleResourceRead(t *testing.T) {
	// Literal JSON, not an encoded apiLifecycleRules: encoding the response
	// through the same struct the code decodes with makes a wrong `json:` tag
	// round-trip through its own mistake and the test stay green. These are the
	// field names storage actually emits (storage/internal/domain/bucket.go).
	const body = `{"rules":[{
		"id":"expire-old",
		"enabled":true,
		"prefix":"logs/",
		"expirationDays":30,
		"noncurrentVersionExpirationDays":90,
		"abortIncompleteMultipartUploadDays":7
	}]}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1/lifecycle" {
			_, _ = w.Write([]byte(body))
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state BucketLifecycleConfigurationModel
	resp.State.Get(context.Background(), &state)
	if len(state.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(state.Rules))
	}
	if state.Rules[0].Prefix.ValueString() != "logs/" {
		t.Errorf("expected prefix logs/, got %v", state.Rules[0].Prefix)
	}
	if state.Rules[0].NoncurrentVersionExpirationDays.ValueInt64() != 90 ||
		state.Rules[0].AbortIncompleteMultipartUploadDays.ValueInt64() != 7 {
		t.Errorf("expected the day counters to decode from the wire field names, got %+v", state.Rules[0])
	}
}

// TestLifecycleRulesRejectsEmptyList pins the validator, not its presence.
// `rules` being Required only means non-null: `rules = []` marshals to
// {"rules":[]}, which the API answers 204 by clearing the bucket's whole
// lifecycle configuration — from a config that reads like it is adding rules.
func TestLifecycleRulesRejectsEmptyList(t *testing.T) {
	s := lifecycleSchema(t)
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

// TestLifecyclePrefixRejectsEmptyString: an empty prefix is dropped from the
// request, so the rule silently applies to EVERY object in the bucket while the
// config reads as if a prefix were set. Refuse it at plan time.
func TestLifecyclePrefixRejectsEmptyString(t *testing.T) {
	s := lifecycleSchema(t)
	rulesAttr := s.Attributes["rules"].(schema.ListNestedAttribute)
	prefix, ok := rulesAttr.NestedObject.Attributes["prefix"].(schema.StringAttribute)
	if !ok {
		t.Fatalf("expected prefix to be a StringAttribute, got %T", rulesAttr.NestedObject.Attributes["prefix"])
	}
	if len(prefix.Validators) == 0 {
		t.Fatal("prefix has no validators")
	}

	resp := &validator.StringResponse{}
	for _, v := range prefix.Validators {
		v.ValidateString(context.Background(),
			validator.StringRequest{Path: path.Root("rules"), ConfigValue: types.StringValue("")}, resp)
	}
	if !resp.Diagnostics.HasError() {
		t.Error("expected an empty prefix to be rejected at plan time")
	}
}

// TestLifecycleReadNotFoundWithLiveBucketErrors covers the 404 that does NOT
// mean "deleted": the service answers 404 when a tenant's object-storage
// account cannot be resolved. Silently forgetting the resource on a backend
// failure makes the next apply blind-write rules that delete objects.
func TestLifecycleReadNotFoundWithLiveBucketErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1" {
			_, _ = w.Write([]byte(`{"name":"b1"}`))
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("expected an error when the sub-resource 404s but the bucket is still present")
	}
	if resp.State.Raw.IsNull() {
		t.Error("state must be kept, not silently dropped, when the bucket still exists")
	}
}

// TestLifecycleDeleteNotFoundWithLiveBucketErrors: a destroy that reports
// success while the rules are still expiring objects is the worst outcome here.
func TestLifecycleDeleteNotFoundWithLiveBucketErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1" {
			_, _ = w.Write([]byte(`{"name":"b1"}`))
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Error("a destroy must not report success when the bucket is still present and the delete 404'd")
	}
}

// TestLifecycleResourceReadEmptyRemovesState covers the drift path that a 404
// does not: a bucket whose lifecycle configuration was deleted outside
// Terraform answers 200 with an empty rule set, not 404.
func TestLifecycleResourceReadEmptyRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/t-123/buckets/b1/lifecycle" {
			_ = json.NewEncoder(w).Encode(apiLifecycleRules{Rules: []apiLifecycleRule{}})
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state when the API returns no rules")
	}
}

func TestLifecycleResourceReadNotFoundRemovesState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: lifecycleValue("gone", minimalRule())}}
	r.Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: lifecycleValue("gone", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected the resource to be removed from state on 404")
	}
}

func TestLifecycleResourceUpdate(t *testing.T) {
	var gotBody apiLifecycleRules
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/t-123/buckets/b1/lifecycle" {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	updated := minimalRule()
	updated["expiration_days"] = tftypes.NewValue(tftypes.Number, 90)

	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{
		State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())},
		Plan:  tfsdk.Plan{Schema: s, Raw: lifecycleValue("b1", updated)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if len(gotBody.Rules) != 1 || gotBody.Rules[0].ExpirationDays != 90 {
		t.Errorf("expected the updated expiration to reach the API, got %+v", gotBody)
	}
}

func TestLifecycleResourceDelete(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/tenants/t-123/buckets/b1/lifecycle" {
			called = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: lifecycleValue("b1", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if !called {
		t.Error("expected DELETE to reach the lifecycle sub-resource")
	}
}

func TestLifecycleResourceDeleteAlreadyGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		notFound(w)
	}))
	defer server.Close()

	r := configuredResource(t, server.URL)
	s := lifecycleSchema(t)

	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: lifecycleValue("gone", minimalRule())}}
	r.Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: lifecycleValue("gone", minimalRule())}}, resp)

	if resp.Diagnostics.HasError() {
		t.Errorf("an already-deleted configuration must not error, got %v", resp.Diagnostics)
	}
}
