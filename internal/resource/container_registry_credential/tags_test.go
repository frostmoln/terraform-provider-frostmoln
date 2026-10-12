package container_registry_credential

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

const credentialBodyTagged = `{"id":7,"username":"robot$t-abc-1","name":"ci","capability":"push",
	"disabled":false,"endpoint":"registry.sweden.frostmoln.cloud","namespaces":["t-abc"],
	"createdAt":"2026-08-28T10:00:00Z","tags":{"env":"prod","portal":"p"}}`

// The create carries the configured tags beside name and capability.
func TestCreateSendsTags(t *testing.T) {
	rec := &recorder{}
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":7,"username":"robot$t-abc-1","name":"ci","capability":"push","endpoint":"e",
			"namespaces":["t-abc"],"createdAt":"c","secret":"s3","tags":{"env":"prod","cost-center":"42"}}`) // pragma: allowlist secret
	})
	defer server.Close()

	s := credentialSchema(t)
	plan := withAttrs(t, credentialState(t, "", ""), map[string]tftypes.Value{
		"id": tftypes.NewValue(tftypes.String, tftypes.UnknownValue), "secret": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"tags": tagVal(map[string]string{"env": "prod"}, false), "tags_all": tagVal(nil, true),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(credentialTFType(t), nil)}}
	configuredCredentialResource(t, server.URL).Create(context.Background(), resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create: %v", resp.Diagnostics)
	}
	if got := sentTags(t, rec.bodies[http.MethodPost]); !mapsEqual(got, map[string]string{"env": "prod"}) {
		t.Errorf("the create sent tags %v, want env=prod", got)
	}
	if all := mapAttr(t, resp.State.Raw, "tags_all"); !mapsEqual(all, map[string]string{"env": "prod", "cost-center": "42"}) {
		t.Errorf("tags_all = %v", all)
	}
}

// A tags change is a PATCH of the credential carrying `tags` alone — never a
// replacement, which would issue a new secret — and the secret in state
// survives it, since no response after the create carries one.
func TestTagsChangeIsAPatchAndKeepsTheSecret(t *testing.T) {
	rec := &recorder{}
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, r *http.Request) {
		body := rec.record(r)
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, credentialBodyTagged)
		case http.MethodPatch:
			var obj map[string]any
			_ = json.Unmarshal([]byte(credentialBodyTagged), &obj)
			obj["tags"] = body["tags"]
			_ = json.NewEncoder(w).Encode(obj)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	})
	defer server.Close()

	s := credentialSchema(t)
	state := withAttrs(t, credentialState(t, "7", "the-only-copy"), map[string]tftypes.Value{ // pragma: allowlist secret
		"tags": tagVal(map[string]string{"env": "prod"}, false), "tags_all": tagVal(map[string]string{"env": "prod", "portal": "p"}, false),
	})
	plan := withAttrs(t, state, map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "staging"}, false), "tags_all": tagVal(nil, true),
	})
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: plan}}
	configuredCredentialResource(t, server.URL).Update(context.Background(), resource.UpdateRequest{
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
		if req != "GET /v1/tenants/tenant-456/registry/credentials/7" && req != "PATCH /v1/tenants/tenant-456/registry/credentials/7" {
			t.Errorf("unexpected request %s", req)
		}
	}
	var attrs map[string]tftypes.Value
	_ = resp.State.Raw.As(&attrs)
	var secret string
	_ = attrs["secret"].As(&secret)
	if secret != "the-only-copy" { // pragma: allowlist secret
		t.Errorf("secret = %q after a tags update, want the state's copy kept", secret)
	}
	if all := mapAttr(t, resp.State.Raw, "tags_all"); !mapsEqual(all, map[string]string{"env": "staging", "portal": "p"}) {
		t.Errorf("tags_all = %v", all)
	}
}

// A refresh that answers the credential but not its tags keeps the tags state
// holds and warns.
func TestReadKeepsTagsWhenTheyAreUnavailable(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":7,"username":"robot$t-abc-1","name":"ci","capability":"push","endpoint":"e",
			"namespaces":["t-abc"],"createdAt":"c","tags":null,"tagsUnavailable":true}`)
	})
	defer server.Close()

	s := credentialSchema(t)
	state := withAttrs(t, credentialState(t, "7", "x"), map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "prod"}, false), "tags_all": tagVal(map[string]string{"env": "prod", "portal": "p"}, false),
	})
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: state.Copy()}}
	configuredCredentialResource(t, server.URL).Read(context.Background(), resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: state}}, resp)
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
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":7,"name":"ci","capability":"push","tags":null,"tagsUnavailable":true}`)
	})
	defer server.Close()

	s := credentialSchema(t)
	state := withAttrs(t, credentialState(t, "7", "x"), map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "prod"}, false), "tags_all": tagVal(map[string]string{"env": "prod"}, false),
	})
	plan := withAttrs(t, state, map[string]tftypes.Value{
		"tags": tagVal(map[string]string{"env": "staging"}, false), "tags_all": tagVal(nil, true),
	})
	resp := &resource.UpdateResponse{State: tfsdk.State{Schema: s, Raw: plan}}
	configuredCredentialResource(t, server.URL).Update(context.Background(), resource.UpdateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: plan}, State: tfsdk.State{Schema: s, Raw: state},
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("the update must fail while the tags cannot be read")
	}
}
