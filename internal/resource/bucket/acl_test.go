package bucket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func strPtr(s string) *string { return &s }

func TestBucketModelToCreateRequestCarriesACL(t *testing.T) {
	m := BucketModel{Name: types.StringValue("b"), ACL: types.StringValue("public-read")}
	if got := m.toCreateRequest().ACL; got != "public-read" {
		t.Errorf("expected acl public-read on create, got %q", got)
	}

	// Unset (unknown at create, as an omitted Optional+Computed plans) is left
	// to the server, which creates the bucket private: the key is not sent.
	for _, acl := range []types.String{types.StringNull(), types.StringUnknown()} {
		m := BucketModel{Name: types.StringValue("b"), ACL: acl}
		body, err := json.Marshal(m.toCreateRequest())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(body), `"acl"`) {
			t.Errorf("an unset acl must not be sent on create, got %s", body)
		}
	}
}

// TestBucketModelToUpdateRequestACL pins when Update writes acl: only when the
// configuration sets it AND the plan changes it. A silent configuration must
// never re-send the state value — that would re-publish a bucket someone made
// private outside Terraform.
func TestBucketModelToUpdateRequestACL(t *testing.T) {
	for _, tc := range []struct {
		name               string
		config, plan, prev types.String
		want               *string
	}{
		{"config silent, state public: nothing sent", types.StringNull(), types.StringValue("public-read"), types.StringValue("public-read"), nil},
		{"config silent, plan unknown: nothing sent", types.StringNull(), types.StringUnknown(), types.StringNull(), nil},
		{"config set, plan equals state: nothing sent", types.StringValue("public-read"), types.StringValue("public-read"), types.StringValue("public-read"), nil},
		{"config unknown: nothing sent", types.StringUnknown(), types.StringUnknown(), types.StringValue("private"), nil},
		{"config private, state public: private sent", types.StringValue("private"), types.StringValue("private"), types.StringValue("public-read"), strPtr("private")},
		{"config public, state private: public sent", types.StringValue("public-read"), types.StringValue("public-read"), types.StringValue("private"), strPtr("public-read")},
		{"config public, state null (import): public sent", types.StringValue("public-read"), types.StringValue("public-read"), types.StringNull(), strPtr("public-read")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := BucketModel{ACL: tc.plan}
			req := plan.toUpdateRequest(&BucketModel{ACL: tc.config}, &BucketModel{ACL: tc.prev})
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if tc.want == nil {
				if strings.Contains(string(body), `"acl"`) {
					t.Errorf("expected no acl key, got %s", body)
				}
				return
			}
			if req.ACL == nil || *req.ACL != *tc.want {
				t.Errorf("expected acl %q, got %s", *tc.want, body)
			}
		})
	}
}

// TestBucketModelFromAPIACL pins the omitted-acl contract: storage leaves
// `acl` out when it could not read the bucket's real policy, and that must
// never become "" (a spurious diff) or "private" (hiding a public bucket).
func TestBucketModelFromAPIACL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prior types.String
		wire  *string
		want  types.String
	}{
		{"present overrides prior", types.StringValue("private"), strPtr("public-read"), types.StringValue("public-read")},
		{"present fills an imported null", types.StringNull(), strPtr("public-read"), types.StringValue("public-read")},
		{"omitted keeps a public prior", types.StringValue("public-read"), nil, types.StringValue("public-read")},
		{"omitted keeps a private prior", types.StringValue("private"), nil, types.StringValue("private")},
		{"omitted keeps an imported null", types.StringNull(), nil, types.StringNull()},
		{"omitted resolves unknown to null", types.StringUnknown(), nil, types.StringNull()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := BucketModel{ACL: tc.prior, Tags: types.MapNull(types.StringType), TagsAll: types.MapNull(types.StringType)}
			diags := m.fromAPI(context.Background(), &apiBucket{Name: "b", ACL: tc.wire})
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if !m.ACL.Equal(tc.want) {
				t.Errorf("expected acl %v, got %v", tc.want, m.ACL)
			}
		})
	}
}

func TestBucketACLDecodesOmittedAsAbsent(t *testing.T) {
	var b apiBucket
	if err := json.Unmarshal([]byte(`{"name":"b","versioning":"disabled"}`), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b.ACL != nil {
		t.Errorf("an omitted acl must decode as absent, got %q", *b.ACL)
	}
}

// bucketVal builds a full object value for the bucket schema: a known state
// by default, with overrides per test (a config sets computed-only ones null).
func bucketVal(overrides map[string]tftypes.Value) tftypes.Value {
	vals := map[string]tftypes.Value{
		"name":          tftypes.NewValue(tftypes.String, "acl-bucket"),
		"region":        tftypes.NewValue(tftypes.String, "sweden"),
		"storage_class": tftypes.NewValue(tftypes.String, "STANDARD"),
		"versioning":    tftypes.NewValue(tftypes.String, "disabled"),
		"acl":           tftypes.NewValue(tftypes.String, "private"),
		"tags":          tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"tags_all":      tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"object_count":  tftypes.NewValue(tftypes.Number, 0),
		"size_bytes":    tftypes.NewValue(tftypes.Number, 0),
		"created_at":    tftypes.NewValue(tftypes.String, "2026-10-01T00:00:00Z"),
	}
	for k, v := range overrides {
		vals[k] = v
	}
	return tftypes.NewValue(bucketObjectType(), vals)
}

func bucketStateVal(acl tftypes.Value) tftypes.Value {
	return bucketVal(map[string]tftypes.Value{"acl": acl})
}

// bucketConfigVal is a configuration: computed-only attributes null, the
// optional ones as given.
func bucketConfigVal(overrides map[string]tftypes.Value) tftypes.Value {
	nullStr := tftypes.NewValue(tftypes.String, nil)
	base := map[string]tftypes.Value{
		"region":        nullStr,
		"storage_class": nullStr,
		"versioning":    nullStr,
		"acl":           nullStr,
		"object_count":  tftypes.NewValue(tftypes.Number, nil),
		"size_bytes":    tftypes.NewValue(tftypes.Number, nil),
		"created_at":    nullStr,
	}
	for k, v := range overrides {
		base[k] = v
	}
	return bucketVal(base)
}

func str(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }

func aclTestResource(t *testing.T, handler http.HandlerFunc) resource.Resource {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	r := NewResource()
	r.(resource.ResourceWithConfigure).Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})
	return r
}

const aclBucketPath = "/v1/tenants/t-123/buckets/acl-bucket"

func warningsOf(diags diag.Diagnostics) []diag.Diagnostic {
	var out []diag.Diagnostic
	for _, d := range diags {
		if d.Severity() == diag.SeverityWarning {
			out = append(out, d)
		}
	}
	return out
}

func readACL(t *testing.T, body string, prior tftypes.Value) (BucketModel, diag.Diagnostics) {
	t.Helper()
	r := aclTestResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == aclBucketPath {
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	s := bucketSchema(t)
	state := tfsdk.State{Schema: s, Raw: bucketStateVal(prior)}
	resp := &resource.ReadResponse{State: state}
	r.Read(context.Background(), resource.ReadRequest{State: state}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	var model BucketModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &model)...)
	return model, resp.Diagnostics
}

// TestBucketResourceReadOmittedACLKeepsPriorAndWarns is the refresh a
// transient failure produces: the bucket is public, its access setting could
// not be read and `acl` is absent. State keeps public-read, and the refresh
// says the value was not verified.
func TestBucketResourceReadOmittedACLKeepsPriorAndWarns(t *testing.T) {
	model, diags := readACL(t,
		`{"name":"acl-bucket","region":"sweden","defaultStorageClass":"STANDARD","versioning":"disabled","createdAt":"2026-10-01T00:00:00Z"}`,
		str("public-read"))
	if model.ACL.ValueString() != "public-read" {
		t.Errorf("an omitted acl must keep the prior public-read, got %v", model.ACL)
	}
	warns := warningsOf(diags)
	if len(warns) != 1 || warns[0].Summary() != aclUnverifiedSummary {
		t.Fatalf("expected one %q warning, got %v", aclUnverifiedSummary, diags)
	}
	if !strings.Contains(warns[0].Detail(), `"public-read"`) {
		t.Errorf("the warning must name the last known value, got %q", warns[0].Detail())
	}
}

func TestBucketResourceReadWithACLDoesNotWarn(t *testing.T) {
	model, diags := readACL(t,
		`{"name":"acl-bucket","acl":"private","versioning":"disabled","defaultStorageClass":"STANDARD"}`,
		str("public-read"))
	if model.ACL.ValueString() != "private" {
		t.Errorf("expected the read acl private, got %v", model.ACL)
	}
	if w := warningsOf(diags); len(w) != 0 {
		t.Errorf("a read carrying acl must not warn, got %v", w)
	}
}

// TestBucketResourceReadAfterImportSetsACL: import leaves only the name in
// state, so the first Read is what fills acl.
func TestBucketResourceReadAfterImportSetsACL(t *testing.T) {
	model, _ := readACL(t,
		`{"name":"acl-bucket","acl":"public-read","versioning":"disabled","defaultStorageClass":"STANDARD"}`,
		tftypes.NewValue(tftypes.String, nil))
	if model.ACL.ValueString() != "public-read" {
		t.Errorf("expected acl public-read after import, got %v", model.ACL)
	}
}

// TestBucketResourceCreateWithoutACL: an omitted acl is not sent, and state
// takes the server's answer (private by default).
func TestBucketResourceCreateWithoutACL(t *testing.T) {
	var body map[string]any
	r := aclTestResource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-123/buckets" {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"acl-bucket","region":"sweden","acl":"private","versioning":"disabled","defaultStorageClass":"STANDARD","createdAt":"2026-10-01T00:00:00Z"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	unknown := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	s := bucketSchema(t)
	plan := tfsdk.Plan{Schema: s, Raw: bucketVal(map[string]tftypes.Value{
		"region": unknown, "storage_class": unknown, "versioning": unknown, "acl": unknown,
		"object_count": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"size_bytes":   tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":   unknown,
	})}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if _, ok := body["acl"]; ok {
		t.Errorf("an omitted acl must not be sent on create, got %v", body)
	}
	var model BucketModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &model)...)
	if model.ACL.ValueString() != "private" {
		t.Errorf("expected state acl private from the response, got %v", model.ACL)
	}
}

// updateACL runs one Update and returns the PUT body as a raw map, so a test
// can assert a key is absent rather than zero.
func updateACL(t *testing.T, config, state, plan tftypes.Value, respBody string) (map[string]any, BucketModel) {
	t.Helper()
	var body map[string]any
	r := aclTestResource(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == aclBucketPath:
			_, _ = w.Write([]byte(respBody))
		case r.Method == http.MethodPut && r.URL.Path == aclBucketPath:
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode: %v", err)
			}
			_, _ = w.Write([]byte(respBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	s := bucketSchema(t)
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s}}
	r.Update(context.Background(), resource.UpdateRequest{
		Config: tfsdk.Config{Schema: s, Raw: config},
		Plan:   tfsdk.Plan{Schema: s, Raw: plan},
		State:  tfsdk.State{Schema: s, Raw: state},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if body == nil {
		t.Fatal("no PUT was sent")
	}
	var model BucketModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &model)...)
	return body, model
}

// TestBucketResourceUpdateACLInPlace: a private -> public-read change is one
// PUT carrying acl, and the bucket is not replaced.
func TestBucketResourceUpdateACLInPlace(t *testing.T) {
	body, model := updateACL(t,
		bucketConfigVal(map[string]tftypes.Value{"acl": str("public-read")}),
		bucketStateVal(str("private")),
		bucketStateVal(str("public-read")),
		`{"name":"acl-bucket","region":"sweden","acl":"public-read","versioning":"disabled","defaultStorageClass":"STANDARD","createdAt":"2026-10-01T00:00:00Z"}`)
	if body["acl"] != "public-read" {
		t.Errorf("expected the PUT to carry acl public-read, got %v", body)
	}
	if model.ACL.ValueString() != "public-read" {
		t.Errorf("expected acl public-read in state, got %v", model.ACL)
	}

	attr, ok := bucketSchema(t).Attributes["acl"]
	if !ok {
		t.Fatal("schema has no acl attribute")
	}
	if !attr.IsOptional() || !attr.IsComputed() {
		t.Error("acl must be Optional+Computed")
	}
}

// TestBucketResourceTagOnlyUpdateSendsNoACLOrVersioning: the configuration
// sets neither acl nor versioning and only the tags change. The PUT must carry
// neither key — re-sending the public-read in state would re-publish a bucket
// made private outside Terraform.
func TestBucketResourceTagOnlyUpdateSendsNoACLOrVersioning(t *testing.T) {
	tags := tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, map[string]tftypes.Value{"env": str("prod")})
	body, _ := updateACL(t,
		bucketConfigVal(map[string]tftypes.Value{"tags": tags}),
		bucketStateVal(str("public-read")),
		bucketVal(map[string]tftypes.Value{
			"acl":        str("public-read"), // UseStateForUnknown
			"versioning": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"tags":       tags,
			"tags_all":   tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, tftypes.UnknownValue),
		}),
		`{"name":"acl-bucket","region":"sweden","acl":"private","versioning":"disabled","defaultStorageClass":"STANDARD","tags":{"env":"prod"},"createdAt":"2026-10-01T00:00:00Z"}`)
	if _, ok := body["acl"]; ok {
		t.Errorf("a tag-only update must not send acl, got %v", body)
	}
	if _, ok := body["versioning"]; ok {
		t.Errorf("a tag-only update must not send versioning, got %v", body)
	}
}

func modifyPlanACL(t *testing.T, configACL, stateACL tftypes.Value) diag.Diagnostics {
	t.Helper()
	r := aclTestResource(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	s := bucketSchema(t)
	state := bucketStateVal(stateACL)
	plan := tfsdk.Plan{Schema: s, Raw: state}
	resp := &resource.ModifyPlanResponse{Plan: plan}
	r.(resource.ResourceWithModifyPlan).ModifyPlan(context.Background(), resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: s, Raw: bucketConfigVal(map[string]tftypes.Value{"acl": configACL})},
		State:  tfsdk.State{Schema: s, Raw: state},
		Plan:   plan,
	}, resp)
	return resp.Diagnostics
}

// TestBucketModifyPlanWarnsWhenAPublicBucketDropsACL: removing
// `acl = "public-read"` from the configuration plans nothing, so the plan must
// say the bucket stays public.
func TestBucketModifyPlanWarnsWhenAPublicBucketDropsACL(t *testing.T) {
	null := tftypes.NewValue(tftypes.String, nil)

	warns := warningsOf(modifyPlanACL(t, null, str("public-read")))
	if len(warns) != 1 || warns[0].Summary() != aclStaysPublicSummary || warns[0].Detail() != aclStaysPublicDetail {
		t.Fatalf("expected one %q warning, got %v", aclStaysPublicSummary, warns)
	}

	for _, tc := range []struct {
		name          string
		config, state tftypes.Value
	}{
		{"configured public-read", str("public-read"), str("public-read")},
		{"configured private", str("private"), str("public-read")},
		{"silent on a private bucket", null, str("private")},
		{"silent on an unread bucket", null, null},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := warningsOf(modifyPlanACL(t, tc.config, tc.state)); len(w) != 0 {
				t.Errorf("expected no warning, got %v", w)
			}
		})
	}
}
