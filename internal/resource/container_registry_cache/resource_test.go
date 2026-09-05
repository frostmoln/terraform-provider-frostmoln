package container_registry_cache

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func newMeAndCacheServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
			return
		}
		handler(w, r)
	}))
}

func configuredCacheResource(t *testing.T, serverURL string) *cacheResource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &cacheResource{client: c}
}

func cacheSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := resource.SchemaResponse{}
	(&cacheResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func cacheTFType(t *testing.T) tftypes.Type {
	t.Helper()
	return cacheSchema(t).Type().TerraformType(context.Background())
}

// cacheStateValue builds a state value. username/password are parameters
// because they are what the whole design turns on: nothing that reads from the
// API may overwrite them.
func cacheStateValue(t *testing.T, upstream, username, password string) tftypes.Value {
	t.Helper()
	str := func(s string) tftypes.Value {
		if s == "" {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, s)
	}
	return tftypes.NewValue(cacheTFType(t), map[string]tftypes.Value{
		"id":        str(upstream),
		"tenant_id": tftypes.NewValue(tftypes.String, "tenant-456"),
		"upstream":  str(upstream),
		"username":  str(username),
		"password":  str(password),
		// A write-only attribute is null in prior state, in the plan and in
		// final state — never a value.
		"password_wo":         tftypes.NewValue(tftypes.String, nil),
		"password_wo_version": tftypes.NewValue(tftypes.String, nil),
		"namespace":           tftypes.NewValue(tftypes.String, "t-abc-cache-"+upstream),
		"pull_path":           tftypes.NewValue(tftypes.String, "registry.sweden.frostmoln.cloud/t-abc-cache-"+upstream),
		"display":             tftypes.NewValue(tftypes.String, "GitHub Container Registry (ghcr.io)"),
	})
}

func readCache(t *testing.T, r *cacheResource, raw tftypes.Value) (*resource.ReadResponse, ContainerRegistryCacheModel) {
	t.Helper()
	s := cacheSchema(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	var got ContainerRegistryCacheModel
	if !resp.State.Raw.IsNull() {
		if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
			t.Fatalf("reading back state: %v", diags)
		}
	}
	return resp, got
}

const ghcrListBody = `{"data":[{"upstream":"ghcr","display":"GitHub Container Registry (ghcr.io)",
	"namespace":"t-abc-cache-ghcr","pullPath":"registry.sweden.frostmoln.cloud/t-abc-cache-ghcr"}],
	"totalCount":1,"limit":5,"upstreams":[{"key":"ghcr","display":"GitHub Container Registry (ghcr.io)",
	"requiresCredentials":false}]}`

// THE contract of this resource. No endpoint returns the upstream credentials,
// so a Read that wrote them from the response would blank them — and with
// RequiresReplace on both, the next plan would DESTROY the cache and every image
// in it, with no configuration change at all.
func TestReadPreservesTheWriteOnlyUpstreamCredentials(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, ghcrListBody)
	})
	defer server.Close()

	// pragma: allowlist nextline secret
	_, got := readCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "acme", "dckr_pat_only_copy"))

	if got.Username.ValueString() != "acme" {
		t.Errorf("username = %q, want it carried across from state", got.Username.ValueString())
	}
	if got.Password.ValueString() != "dckr_pat_only_copy" { // pragma: allowlist secret
		t.Errorf("password = %q, want it carried across from state", got.Password.ValueString())
	}
	// And the rest of the state must still come from the response.
	if got.PullPath.ValueString() != "registry.sweden.frostmoln.cloud/t-abc-cache-ghcr" {
		t.Errorf("pull_path = %q", got.PullPath.ValueString())
	}
	if got.TenantID.ValueString() != "tenant-456" {
		t.Errorf("tenant_id = %q, want the provider's operating tenant", got.TenantID.ValueString())
	}
}

// A cache with no credentials must not gain empty-string ones on a read either:
// null and "" are different plans, and "" would show as a diff forever.
func TestReadLeavesAbsentCredentialsNull(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, ghcrListBody)
	})
	defer server.Close()

	_, got := readCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "", ""))

	if !got.Username.IsNull() {
		t.Errorf("username = %q, want null", got.Username.ValueString())
	}
	if !got.Password.IsNull() {
		t.Errorf("password = %q, want null", got.Password.ValueString())
	}
}

// The listing is the ONLY read surface — there is no single-cache GET — so
// absence from a listing the server actually answered is what "deleted" means.
func TestReadRemovesACacheMissingFromTheListing(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"totalCount":0,"limit":5,"upstreams":[]}`)
	})
	defer server.Close()

	resp, _ := readCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "", ""))

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a cache absent from the listing should be removed from state")
	}
}

// REGISTRY_NOT_ENABLED is a statement about the REGISTRY, never about this
// cache. Dropping the resource on it would plan a create of a cache that may
// well still exist, which then fails with CACHE_ALREADY_EXISTS.
func TestReadKeepsTheCacheWhenTheRegistryIsNotEnabled(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"invalid_state","message":"no registry",
			"details":{"reason":"REGISTRY_NOT_ENABLED"}}`)
	})
	defer server.Close()

	resp, _ := readCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "", ""))

	if !resp.Diagnostics.HasError() {
		t.Fatal("a registry-level refusal must be an error, not a silent state removal")
	}
	if resp.State.Raw.IsNull() {
		t.Error("the cache must stay in state on a registry-level refusal")
	}
}

// A short listing is invisible: a cache missing from it looks exactly like a
// cache that was deleted. totalCount is the only cross-check this unpaginated
// response carries, so it is read rather than decoded and ignored.
func TestReadRefusesAListingShorterThanItsOwnCount(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"totalCount":3,"limit":5,"upstreams":[]}`)
	})
	defer server.Close()

	resp, _ := readCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "", ""))

	if !resp.Diagnostics.HasError() {
		t.Fatal("a listing shorter than its own totalCount must be refused, not acted on")
	}
	if resp.State.Raw.IsNull() {
		t.Error("the cache must stay in state when the listing could not be trusted")
	}
}

// A cache whose upstream has been RETIRED from the catalog still exists, still
// lists, and stays deletable — its display just falls back to the bare key.
func TestReadHandlesARetiredUpstreamWhoseDisplayFellBackToTheKey(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Note the empty `upstreams` catalog: the retired key has no row there.
		_, _ = io.WriteString(w, `{"data":[{"upstream":"legacyhub","display":"legacyhub",
			"namespace":"t-abc-cache-legacyhub",
			"pullPath":"registry.sweden.frostmoln.cloud/t-abc-cache-legacyhub"}],
			"totalCount":1,"limit":5,"upstreams":[]}`)
	})
	defer server.Close()

	resp, got := readCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "legacyhub", "", ""))

	if resp.Diagnostics.HasError() {
		t.Fatalf("a retired upstream must not be an error: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("a cache on a retired upstream must stay in state")
	}
	if got.Display.ValueString() != "legacyhub" {
		t.Errorf("display = %q, want the bare key", got.Display.ValueString())
	}
}

// createCache drives Create with cfg as BOTH the config and the plan, except
// that the plan's write-only attribute is forced to null — which is what
// Terraform itself does. That asymmetry is the whole point of the write-only
// path, so the helper reproduces it rather than passing one value twice.
func createCache(t *testing.T, r *cacheResource, cfg ContainerRegistryCacheModel) *resource.CreateResponse {
	t.Helper()
	s := cacheSchema(t)

	raw := func(m ContainerRegistryCacheModel) tftypes.Value {
		p := tfsdk.Plan{Schema: s}
		if diags := p.Set(context.Background(), &m); diags.HasError() {
			t.Fatalf("building value: %v", diags)
		}
		return p.Raw
	}

	config := tfsdk.Config{Schema: s, Raw: raw(cfg)}

	planModel := cfg
	planModel.PasswordWO = types.StringNull()
	planState := tfsdk.Plan{Schema: s, Raw: raw(planModel)}

	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: planState, Config: config}, resp)
	return resp
}

// The request body is asserted by DECODING it, not by matching a string: a
// wire-encoding mistake is exactly the class of bug a fake that ignores the body
// cannot see.
func TestCreateSendsTheUpstreamKeyAndTheCredentials(t *testing.T) {
	var body apiCreateCacheRequest
	var rawBody map[string]any
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_ = json.Unmarshal(raw, &rawBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"upstream":"dockerhub","display":"Docker Hub (docker.io)",
			"namespace":"t-abc-cache-dockerhub",
			"pullPath":"registry.sweden.frostmoln.cloud/t-abc-cache-dockerhub"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream: types.StringValue("dockerhub"),
		Username: types.StringValue("acme"),
		Password: types.StringValue("dckr_pat_x"), // pragma: allowlist secret
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}

	if body.Upstream != "dockerhub" || body.Username != "acme" || body.Password != "dckr_pat_x" { // pragma: allowlist secret
		t.Errorf("request body = %+v", body)
	}

	var got ContainerRegistryCacheModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	// The credentials came from the plan and must survive into state: the 201
	// body does not carry them, so fromAPI cannot put them there.
	if got.Password.ValueString() != "dckr_pat_x" { // pragma: allowlist secret
		t.Errorf("password = %q, want it carried from the plan", got.Password.ValueString())
	}
	if got.ID.ValueString() != "dockerhub" {
		t.Errorf("id = %q, want the upstream key", got.ID.ValueString())
	}
}

// An upstream that needs no credentials must send NO credential keys. Empty
// strings would be stored as a real (broken) credential pair rather than as
// "none", and no endpoint can read them back to reveal it.
func TestCreateOmitsCredentialsWhenNoneAreConfigured(t *testing.T) {
	var rawBody map[string]any
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &rawBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"upstream":"ghcr","display":"GitHub Container Registry (ghcr.io)",
			"namespace":"t-abc-cache-ghcr",
			"pullPath":"registry.sweden.frostmoln.cloud/t-abc-cache-ghcr"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream: types.StringValue("ghcr"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if _, ok := rawBody["username"]; ok {
		t.Errorf("username was sent for an upstream with no credentials: %v", rawBody)
	}
	if _, ok := rawBody["password"]; ok {
		t.Errorf("password was sent for an upstream with no credentials: %v", rawBody)
	}
}

// A body for a different upstream would put one cache's identity in another's
// state, and the next destroy would delete the wrong namespace with its images.
func TestCreateRefusesAResponseForADifferentUpstream(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"upstream":"quay","display":"Quay (quay.io)",
			"namespace":"t-abc-cache-quay","pullPath":"x/t-abc-cache-quay"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream: types.StringValue("ghcr"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a create response for another upstream must be refused")
	}
}

func TestCreateRefusesAResponseWithoutAPullPath(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"upstream":"ghcr","display":"x","namespace":"t-abc-cache-ghcr"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream: types.StringValue("ghcr"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("a cache with no pull path is the whole product of the resource missing")
	}
}

// Every refusal branches on `details.reason` or on the error CODE, never on the
// message text — the messages here are deliberately unhelpful so a test that
// passed by reading them would fail.
func TestCreateRefusalsBranchOnTheReasonNotTheMessage(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{
			name:    "registry not enabled",
			status:  http.StatusConflict,
			body:    `{"code":"invalid_state","message":"x","details":{"reason":"REGISTRY_NOT_ENABLED"}}`,
			wantSub: "not enabled",
		},
		{
			name:    "cache already exists",
			status:  http.StatusConflict,
			body:    `{"code":"conflict","message":"x","details":{"reason":"CACHE_ALREADY_EXISTS"}}`,
			wantSub: "terraform import",
		},
		{
			name:   "upstream not permitted names the server's own list",
			status: http.StatusBadRequest,
			body: `{"code":"invalid_request","message":"x","details":{"reason":"UPSTREAM_NOT_PERMITTED",
				"permitted":["quay","ghcr","dockerhub"]}}`,
			wantSub: "dockerhub, ghcr, quay",
		},
		{
			name:    "upstream credentials required",
			status:  http.StatusBadRequest,
			body:    `{"code":"invalid_request","message":"x","details":{"reason":"UPSTREAM_CREDENTIALS_REQUIRED","upstream":"dockerhub"}}`,
			wantSub: "requires your own credentials",
		},
		{
			// The cap is a 403 quota_exceeded, NOT a 409. Branching on the status
			// alone never sees it.
			name:    "cache cap",
			status:  http.StatusForbidden,
			body:    `{"code":"quota_exceeded","message":"x"}`,
			wantSub: "maximum number of container registry caches",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			defer server.Close()

			resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
				Upstream: types.StringValue("dockerhub"),
			})
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error")
			}
			all := resp.Diagnostics.Errors()[0].Summary() + "\n" + resp.Diagnostics.Errors()[0].Detail()
			if !strings.Contains(strings.ToLower(all), strings.ToLower(tc.wantSub)) {
				t.Errorf("diagnostic did not name the refusal.\ngot: %s\nwant substring: %s", all, tc.wantSub)
			}
		})
	}
}

func deleteCache(t *testing.T, r *cacheResource, raw tftypes.Value) *resource.DeleteResponse {
	t.Helper()
	s := cacheSchema(t)
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}
	r.Delete(context.Background(), resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)
	return resp
}

// The upstream is a QUERY parameter, not a path segment. Sent as a path segment
// it would 404; omitted, the DELETE addresses the collection.
func TestDeleteSendsTheUpstreamAsAQueryParameter(t *testing.T) {
	var gotPath, gotQuery, gotMethod string
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.Query().Get("upstream")
		w.WriteHeader(http.StatusNoContent)
	})
	defer server.Close()

	resp := deleteCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "", ""))
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/v1/tenants/tenant-456/registry/caches" {
		t.Errorf("path = %s, want the two-segment collection with no upstream in it", gotPath)
	}
	if gotQuery != "ghcr" {
		t.Errorf("upstream query = %q", gotQuery)
	}
}

func TestDeleteTreatsA404AsAlreadyGone(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"not_found","message":"no such cache"}`)
	})
	defer server.Close()

	resp := deleteCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "", ""))
	if resp.Diagnostics.HasError() {
		t.Errorf("a 404 on delete means the cache is already gone: %v", resp.Diagnostics)
	}
}

// Swallowing this would print a successful destroy while the namespace, its
// cached images and the stored upstream credentials all survived.
func TestDeleteRefusesToReportSuccessOnARegistryLevelRefusal(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"invalid_state","message":"x","details":{"reason":"REGISTRY_NOT_ENABLED"}}`)
	})
	defer server.Close()

	resp := deleteCache(t, configuredCacheResource(t, server.URL), cacheStateValue(t, "ghcr", "", ""))
	if !resp.Diagnostics.HasError() {
		t.Fatal("REGISTRY_NOT_ENABLED on delete must not be reported as a successful destroy")
	}
}

func TestImportStateSetsTheIDAndTheUpstream(t *testing.T) {
	s := cacheSchema(t)
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(cacheTFType(t), nil)},
	}
	(&cacheResource{}).ImportState(context.Background(),
		resource.ImportStateRequest{ID: "ghcr"}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	var got ContainerRegistryCacheModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if got.ID.ValueString() != "ghcr" || got.Upstream.ValueString() != "ghcr" {
		t.Errorf("id = %q, upstream = %q", got.ID.ValueString(), got.Upstream.ValueString())
	}
	// Nothing can recover the credentials — no endpoint returns them.
	if !got.Username.IsNull() || !got.Password.IsNull() {
		t.Error("import must not invent credentials it cannot know")
	}
}

func TestImportStateRefusesAnEmptyID(t *testing.T) {
	s := cacheSchema(t)
	resp := &resource.ImportStateResponse{
		State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(cacheTFType(t), nil)},
	}
	(&cacheResource{}).ImportState(context.Background(), resource.ImportStateRequest{ID: "  "}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("an empty import id must be refused")
	}
}

// THE WORST BUG AVAILABLE IN THIS RESOURCE, asserted at the schema. The API has
// no update route, so an attribute that can change without RequiresReplace makes
// `terraform apply` report success and change NOTHING on the server — silently,
// and for `password` that means the practitioner believes they rotated a
// credential they did not.
//
// It is asserted BEHAVIOURALLY (run the modifiers on an old -> new change)
// rather than by matching a plan-modifier type name, so RequiresReplaceIf and
// any other spelling would also pass, and a modifier list reordered or wrapped
// would not produce a false failure.
func TestEveryConfigurableAttributeRequiresReplace(t *testing.T) {
	ctx := context.Background()
	s := cacheSchema(t)

	stateRaw := cacheStateValue(t, "ghcr", "olduser", "oldpass") // pragma: allowlist secret
	planRaw := cacheStateValue(t, "quay", "newuser", "newpass")  // pragma: allowlist secret

	configurable, writeOnly := 0, 0
	for name, attr := range s.Attributes {
		if !attr.IsRequired() && !attr.IsOptional() {
			continue
		}
		configurable++
		strAttr, ok := attr.(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is configurable but not a StringAttribute (%T) — extend this test", name, attr)
		}
		// A WriteOnly attribute is exempt, and must be: it is null in prior
		// state, in the plan and in final state, so a plan modifier on it would
		// compare null against null and could never fire. Its version companion
		// carries the replacement instead, and IS swept below like any other.
		// The provider-level write-only contract asserts it has none.
		if attr.IsWriteOnly() {
			writeOnly++
			if len(strAttr.PlanModifiers) != 0 {
				t.Errorf("%s is WriteOnly and must carry no plan modifiers", name)
			}
			continue
		}
		if len(strAttr.PlanModifiers) == 0 {
			t.Errorf("%s is configurable and carries NO plan modifiers: a change to it would be "+
				"applied as an in-place update the API cannot perform", name)
			continue
		}

		req := planmodifier.StringRequest{
			Path:        path.Root(name),
			State:       tfsdk.State{Schema: s, Raw: stateRaw},
			Plan:        tfsdk.Plan{Schema: s, Raw: planRaw},
			StateValue:  types.StringValue("old"),
			PlanValue:   types.StringValue("new"),
			ConfigValue: types.StringValue("new"),
		}
		modResp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		for _, m := range strAttr.PlanModifiers {
			m.PlanModifyString(ctx, req, modResp)
		}
		if !modResp.RequiresReplace {
			t.Errorf("%s does not require replacement when it changes. The API has NO update route: "+
				"terraform apply would report success and change nothing on the server", name)
		}
	}

	// Guard the guard: if the schema is refactored so that nothing is
	// configurable, the loop above passes vacuously.
	if configurable != 5 {
		t.Errorf("expected 5 configurable attributes (upstream, username, password, password_wo, "+
			"password_wo_version), found %d — a new one must be checked for RequiresReplace too", configurable)
	}
	if writeOnly != 1 {
		t.Errorf("expected exactly 1 write-only attribute to be exempted, found %d", writeOnly)
	}
}

// The upstream catalog is server-owned and rows are retired without a provider
// release, so the provider must hold NO copy of it — not in a validator, not in
// an enum. A OneOf here would refuse, at plan time, a key the server accepts.
func TestUpstreamIsNotValidatedAgainstAHardcodedAllowList(t *testing.T) {
	s := cacheSchema(t)
	upstream, ok := s.Attributes["upstream"].(schema.StringAttribute)
	if !ok {
		t.Fatal("upstream is not a StringAttribute")
	}

	// A OneOf validator states its allowed set in its own description; a length
	// validator does not. This catches the shape of the mistake rather than one
	// helper's name.
	for _, v := range upstream.Validators {
		desc := v.Description(context.Background())
		if strings.Contains(strings.ToLower(desc), "one of") {
			t.Errorf("upstream carries a value allow-list (%q). The catalog is server-owned: read it "+
				"from the frostmoln_container_registry_cache_upstreams data source instead", desc)
		}
	}
}

// The write-only password must reach the WIRE and must NOT reach state. Reading
// it from the plan instead of the config is the mistake that silently creates a
// cache with no credentials — and no endpoint can read the stored credential
// back to reveal it.
func TestCreateSendsThePasswordFromTheWriteOnlyAttributeAndKeepsItOutOfState(t *testing.T) {
	var body apiCreateCacheRequest
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"upstream":"dockerhub","display":"Docker Hub (docker.io)",
			"namespace":"t-abc-cache-dockerhub",
			"pullPath":"registry.sweden.frostmoln.cloud/t-abc-cache-dockerhub"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream:          types.StringValue("dockerhub"),
		Username:          types.StringValue("acme"),
		PasswordWO:        types.StringValue("never-in-state"), // pragma: allowlist secret
		PasswordWOVersion: types.StringValue("1"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if body.Password != "never-in-state" { // pragma: allowlist secret
		t.Errorf("request password = %q, want the write-only value", body.Password)
	}

	var got ContainerRegistryCacheModel
	if diags := resp.State.Get(context.Background(), &got); diags.HasError() {
		t.Fatalf("reading back state: %v", diags)
	}
	if !got.PasswordWO.IsNull() {
		t.Errorf("password_wo = %q in final state; a write-only value must never be written there",
			got.PasswordWO.ValueString())
	}
	if !got.Password.IsNull() {
		t.Errorf("password = %q in final state; the write-only value must not leak into the legacy twin",
			got.Password.ValueString())
	}
	// The version companion IS stored: it is the only change signal there is.
	if got.PasswordWOVersion.ValueString() != "1" {
		t.Errorf("password_wo_version = %q, want it stored in state", got.PasswordWOVersion.ValueString())
	}
}

// Half a credential is ACCEPTED AND STORED by the registry, and nothing reads it
// back — so the failure surfaces later as a failed pull through a cache that
// cannot be repaired in place, only replaced.
func TestCreateRefusesHalfACredential(t *testing.T) {
	cases := map[string]ContainerRegistryCacheModel{
		"username with no password": {
			Upstream: types.StringValue("dockerhub"),
			Username: types.StringValue("acme"),
		},
		"legacy password with no username": {
			Upstream: types.StringValue("dockerhub"),
			Password: types.StringValue("x"), // pragma: allowlist secret
		},
		"write-only password with no username": {
			Upstream:          types.StringValue("dockerhub"),
			PasswordWO:        types.StringValue("x"), // pragma: allowlist secret
			PasswordWOVersion: types.StringValue("1"),
		},
	}

	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("half a credential must stop before the platform is called, got %s %s",
					r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			})
			defer server.Close()

			resp := createCache(t, configuredCacheResource(t, server.URL), plan)
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected a refusal")
			}
		})
	}
}

// Neither half supplied is the ordinary case for an upstream that needs no
// credentials, and must reach the platform. It is the counterpart of the test
// above: a pairing guard that refused this would break every anonymous cache.
func TestCreateAllowsNeitherHalfOfTheCredential(t *testing.T) {
	called := false
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"upstream":"ghcr","display":"GitHub Container Registry (ghcr.io)",
			"namespace":"t-abc-cache-ghcr",
			"pullPath":"registry.sweden.frostmoln.cloud/t-abc-cache-ghcr"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream: types.StringValue("ghcr"),
	})
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error: %v", resp.Diagnostics)
	}
	if !called {
		t.Error("a cache with no credentials must still be created")
	}
}

// The resource type name is a public contract: renaming it breaks every
// configuration and every state file that holds one.
func TestMetadataNamesTheResource(t *testing.T) {
	var resp resource.MetadataResponse
	NewResource().Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_container_registry_cache" {
		t.Errorf("TypeName = %q", resp.TypeName)
	}
}

func TestConfigureRefusesTheWrongProviderData(t *testing.T) {
	r := &cacheResource{}

	// A nil ProviderData is the provider not being configured YET, which the
	// framework does on purpose — it must be tolerated, not refused.
	var ok resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &ok)
	if ok.Diagnostics.HasError() {
		t.Errorf("a nil ProviderData must be tolerated: %v", ok.Diagnostics)
	}

	var bad resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not a client"}, &bad)
	if !bad.Diagnostics.HasError() {
		t.Error("a ProviderData of the wrong type must be refused")
	}
}

// Update is unreachable — every configurable attribute is RequiresReplace — and
// must SAY so rather than silently succeed, which would report an applied change
// the API has no route to make.
func TestUpdateRefusesRatherThanSilentlySucceeding(t *testing.T) {
	var resp resource.UpdateResponse
	(&cacheResource{}).Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: there is no update route, so a success would be a lie")
	}
}

// Both guards exist because the address is the only thing standing between a
// destroy and either a no-op reported as success (Read) or a DELETE on the
// COLLECTION path (Delete).
func TestAnEmptyUpstreamInStateIsRefusedRatherThanActedOn(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a cache with no upstream must not reach the platform, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	r := configuredCacheResource(t, server.URL)
	state := cacheStateValue(t, "", "", "")

	readResp, _ := readCache(t, r, state)
	if !readResp.Diagnostics.HasError() {
		t.Error("Read must refuse a cache it cannot address")
	}
	if readResp.State.Raw.IsNull() {
		t.Error("Read must not remove a cache it could not even look up")
	}

	if delResp := deleteCache(t, r, state); !delResp.Diagnostics.HasError() {
		t.Error("Delete must refuse a cache it cannot address, not DELETE the collection")
	}
}

// A refusal that carries no `details` at all must fall through to the generic
// message rather than panic or be mistaken for a known reason.
func TestCreateFallsBackForARefusalWithNoDetails(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"code":"internal_error","message":"boom"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream: types.StringValue("ghcr"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); summary != "Failed to create the container registry cache" {
		t.Errorf("summary = %q, want the generic failure", summary)
	}
}

// `details.permitted` is rendered verbatim to a practitioner who will paste a
// key out of it, so a non-string entry must be skipped rather than printed
// through Go's %v — and a permitted list that is absent or unusable must not
// produce a dangling "The server currently permits:" with nothing after it.
func TestPermittedSuffixIsOmittedWhenThereIsNothingUsableToName(t *testing.T) {
	cases := map[string]string{
		"no permitted key":      `{"code":"invalid_request","message":"x","details":{"reason":"UPSTREAM_NOT_PERMITTED"}}`,
		"permitted not a list":  `{"code":"invalid_request","message":"x","details":{"reason":"UPSTREAM_NOT_PERMITTED","permitted":"ghcr"}}`,
		"permitted empty":       `{"code":"invalid_request","message":"x","details":{"reason":"UPSTREAM_NOT_PERMITTED","permitted":[]}}`,
		"permitted non-strings": `{"code":"invalid_request","message":"x","details":{"reason":"UPSTREAM_NOT_PERMITTED","permitted":[1,2]}}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, body)
			})
			defer server.Close()

			resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
				Upstream: types.StringValue("nope"),
			})
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error")
			}
			detail := resp.Diagnostics.Errors()[0].Detail()
			if strings.Contains(detail, "currently permits") {
				t.Errorf("named a permitted list it does not have: %s", detail)
			}
		})
	}
}

// A non-API error (a transport failure, a DNS error) reaches the same
// predicates. They must answer "no" rather than mistake it for a known refusal.
func TestRefusalPredicatesIgnoreANonAPIError(t *testing.T) {
	err := errors.New("connection refused")
	if hasReason(err, reasonNotEnabled) {
		t.Error("hasReason matched a plain error")
	}
	if isCacheCapReached(err) {
		t.Error("isCacheCapReached matched a plain error")
	}
	if permittedSuffix(err) != "" {
		t.Error("permittedSuffix named a list on a plain error")
	}
}

// A 403 that is NOT the cap must not be reported as the cap: the entitlement
// refusal is a 403 too, and telling a tenant without the entitlement to delete
// a cache would send them the wrong way entirely.
func TestAForbiddenThatIsNotTheCapIsNotReportedAsTheCap(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"forbidden","message":"feature not enabled for this tenant"}`)
	})
	defer server.Close()

	resp := createCache(t, configuredCacheResource(t, server.URL), ContainerRegistryCacheModel{
		Upstream: types.StringValue("ghcr"),
	})
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); strings.Contains(summary, "maximum number") {
		t.Errorf("a plain 403 was reported as the cache cap: %q", summary)
	}
}
