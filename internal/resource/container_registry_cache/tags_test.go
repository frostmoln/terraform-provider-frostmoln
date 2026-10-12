package container_registry_cache

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func cacheListBody(tags string) string {
	return `{"data":[{"upstream":"ghcr","display":"GitHub Container Registry (ghcr.io)","namespace":"t-abc-cache-ghcr",
		"pullPath":"registry.sweden.frostmoln.cloud/t-abc-cache-ghcr",` + tags + `}],"totalCount":1,"limit":10}`
}

// A tags change is a PATCH addressed by `?upstream=` carrying `tags` alone —
// never a replacement, which would delete every cached image — computed against
// the tags the cache holds now, and the stored upstream credentials in state are
// untouched.
func TestTagsChangeUpdatesInPlace(t *testing.T) {
	rec := &recorder{}
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := rec.record(r)
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, cacheListBody(`"tags":{"env":"prod","portal":"p"}`))
		case http.MethodPatch:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"upstream": "ghcr", "display": "GitHub Container Registry (ghcr.io)", "namespace": "t-abc-cache-ghcr",
				"pullPath": "registry.sweden.frostmoln.cloud/t-abc-cache-ghcr", "tags": body["tags"],
			})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	defer server.Close()

	s := cacheSchema(t)
	state := withAttrs(t, cacheStateValue(t, "ghcr", "acme", "pat"), map[string]tftypes.Value{ // pragma: allowlist secret
		"tags": tagVal(map[string]string{"env": "prod"}, false), "tags_all": tagVal(map[string]string{"env": "prod", "portal": "p"}, false),
	})
	plan := withAttrs(t, state, map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "staging"}, false), "tags_all": tagVal(nil, true),
		"used_bytes": tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
	})
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: plan}}
	configuredCacheResource(t, server.URL).Update(context.Background(), resource.UpdateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: plan}, State: tfsdk.State{Schema: s, Raw: state},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("update: %v", resp.Diagnostics)
	}
	patch := rec.bodies[http.MethodPatch]
	if len(patch) != 1 {
		t.Errorf("the PATCH carried %v, want `tags` alone", patch)
	}
	if got := sentTags(t, patch); !mapsEqual(got, map[string]string{"env": "staging", "portal": "p"}) {
		t.Errorf("the PATCH sent %v, want env=staging with the portal key kept", got)
	}
	for _, req := range rec.requests {
		if req != "GET /v1/tenants/tenant-456/registry/caches" && req != "PATCH /v1/tenants/tenant-456/registry/caches?upstream=ghcr" {
			t.Errorf("unexpected request %s", req)
		}
	}
	var attrs map[string]tftypes.Value
	_ = resp.State.Raw.As(&attrs)
	var username, password string
	_ = attrs["username"].As(&username)
	_ = attrs["password"].As(&password)
	if username != "acme" || password != "pat" { // pragma: allowlist secret
		t.Errorf("username/password = %q/%q after a tags update, want state's values kept", username, password)
	}
	if all := mapAttr(t, resp.State.Raw, "tags_all"); !mapsEqual(all, map[string]string{"env": "staging", "portal": "p"}) {
		t.Errorf("tags_all = %v", all)
	}
}

// A refresh whose listing answers the cache but not its tags keeps the tags
// state holds and warns.
func TestReadKeepsTagsWhenTheyAreUnavailable(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, cacheListBody(`"tags":null,"tagsUnavailable":true`))
	})
	defer server.Close()

	state := withAttrs(t, cacheStateValue(t, "ghcr", "", ""), map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "prod"}, false), "tags_all": tagVal(map[string]string{"env": "prod", "portal": "p"}, false),
	})
	resp, _ := readCache(t, configuredCacheResource(t, server.URL), state)
	if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() != 1 {
		t.Fatalf("diagnostics = %v, want one warning and no error", resp.Diagnostics)
	}
	if tags := mapAttr(t, resp.State.Raw, "tags"); !mapsEqual(tags, map[string]string{"env": "prod"}) {
		t.Errorf("tags = %v, want the prior value kept", tags)
	}
	if all := mapAttr(t, resp.State.Raw, "tags_all"); !mapsEqual(all, map[string]string{"env": "prod", "portal": "p"}) {
		t.Errorf("tags_all = %v, want the prior value kept", all)
	}
}

// An update cannot compute its write against tags it cannot read: it fails and
// sends nothing.
func TestUpdateRefusesWhenTagsAreUnavailable(t *testing.T) {
	server := newMeAndCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, cacheListBody(`"tags":null,"tagsUnavailable":true`))
	})
	defer server.Close()

	s := cacheSchema(t)
	state := withAttrs(t, cacheStateValue(t, "ghcr", "", ""), map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "prod"}, false), "tags_all": tagVal(map[string]string{"env": "prod"}, false),
	})
	plan := withAttrs(t, state, map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "staging"}, false), "tags_all": tagVal(nil, true),
	})
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: plan}}
	configuredCacheResource(t, server.URL).Update(context.Background(), resource.UpdateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: plan}, State: tfsdk.State{Schema: s, Raw: state},
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("the update must fail while the tags cannot be read")
	}
}
