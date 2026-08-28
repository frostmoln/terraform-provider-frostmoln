package container_registry_credential

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func newMeAndCredentialServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
			return
		}
		handler(w, r)
	}))
}

func configuredCredentialResource(t *testing.T, serverURL string) *credentialResource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &credentialResource{client: c}
}

func credentialSchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := resource.SchemaResponse{}
	(&credentialResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func credentialTFType(t *testing.T) tftypes.Type {
	t.Helper()
	return credentialSchema(t).Type().TerraformType(context.Background())
}

// credentialState builds a state value. secret is the field the whole design
// turns on, so it is a parameter rather than a constant.
func credentialState(t *testing.T, id, secret string) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(credentialTFType(t), map[string]tftypes.Value{
		"id":         tftypes.NewValue(tftypes.String, id),
		"name":       tftypes.NewValue(tftypes.String, "ci"),
		"capability": tftypes.NewValue(tftypes.String, "push"),
		"username":   tftypes.NewValue(tftypes.String, "robot$t-abc-1"),
		"secret":     tftypes.NewValue(tftypes.String, secret),
		"disabled":   tftypes.NewValue(tftypes.Bool, false),
		"endpoint":   tftypes.NewValue(tftypes.String, "registry.sweden.frostmoln.cloud"),
		"namespaces": tftypes.NewValue(
			tftypes.List{ElementType: tftypes.String},
			[]tftypes.Value{tftypes.NewValue(tftypes.String, "t-abc")},
		),
		"created_at": tftypes.NewValue(tftypes.String, "2026-08-28T10:00:00Z"),
	})
}

const credentialBodyNoSecret = `{"id":1,"username":"robot$t-abc-1","name":"ci","capability":"push",
	"disabled":false,"endpoint":"registry.sweden.frostmoln.cloud","namespaces":["t-abc"],
	"createdAt":"2026-08-28T10:00:00Z"}`

// THE contract of this resource: reads never carry the secret, so Read must
// carry it across from state. Losing it here loses it permanently — there is no
// rotation endpoint and no retrievable copy anywhere.
func TestReadPreservesTheSecretTheAPINeverReturns(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, credentialBodyNoSecret)
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "1", "the-only-copy") // pragma: allowlist secret
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}

	var got ContainerRegistryCredentialModel
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("state get: %v", d.Errors())
	}
	if got.Secret.ValueString() != "the-only-copy" { // pragma: allowlist secret
		t.Errorf("secret = %q, want it carried across from state", got.Secret.ValueString())
	}
}

// A body that is not the credential we asked for must never rewrite the
// resource's identity — a misrouted path collapsing to the collection would
// otherwise blank the id and strand the credential along with its secret.
func TestReadRefusesAMismatchedCredential(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id":99,"username":"robot$other","name":"other","capability":"pull",
			"endpoint":"registry.sweden.frostmoln.cloud","namespaces":["t-abc"],
			"createdAt":"2026-08-28T10:00:00Z"}`)
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "1", "the-only-copy") // pragma: allowlist secret
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a refusal when the API answers with a different credential")
	}
	if resp.State.Raw.IsNull() {
		t.Error("state was dropped; the credential is still live and its secret unrecoverable")
	}
}

// A create whose response carries no secret must FAIL rather than record a
// credential whose secret is empty: the real secret is already gone, and a
// recorded empty one reads as known-and-correct on every later plan.
func TestCreateFailsWhenNoSecretComesBack(t *testing.T) {
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, credentialBodyNoSecret)
	})
	defer server.Close()

	s := credentialSchema(t)
	plan := credentialState(t, "", "")
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(credentialTFType(t), nil)}}

	configuredCredentialResource(t, server.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: plan}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a create with no secret to fail")
	}
}

// Delete with an empty id would issue DELETE on the COLLECTION, which answers
// 404 and would be swallowed as "already gone" — a reported destroy that did
// not happen, leaving a live credential.
func TestDeleteRefusesAnEmptyID(t *testing.T) {
	var called bool
	server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	})
	defer server.Close()

	s := credentialSchema(t)
	raw := credentialState(t, "", "x")
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredCredentialResource(t, server.URL).Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an empty id to be refused, not sent as a collection DELETE")
	}
	if called {
		t.Error("a request was issued for an unaddressable credential")
	}
}

// The three refusals must be told apart. Both registry-state cases are 409
// differing only in details.reason; the cap is a 403 a status-only reader misses.
func TestCreateRefusalsAreNamed(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{
			"not enabled", http.StatusConflict,
			`{"code":"invalid_state","message":"not enabled","details":{"reason":"REGISTRY_NOT_ENABLED"}}`,
			"is not enabled",
		},
		{
			"credential cap", http.StatusForbidden,
			`{"code":"quota_exceeded","message":"credential limit reached"}`,
			"maximum number",
		},
		{
			// An unrelated conflict must NOT be dressed up as the not-enabled
			// remedy: telling someone to create a registry they already have
			// sends them the wrong way.
			"unrelated conflict", http.StatusConflict,
			`{"code":"conflict","message":"something else"}`,
			"Failed to create",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newMeAndCredentialServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			defer server.Close()

			s := credentialSchema(t)
			resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(credentialTFType(t), nil)}}

			configuredCredentialResource(t, server.URL).Create(context.Background(),
				resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: credentialState(t, "", "")}}, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatal("expected an error")
			}
			var summaries string
			for _, d := range resp.Diagnostics.Errors() {
				summaries += d.Summary() + "|"
			}
			if !strings.Contains(summaries, tc.wantSub) {
				t.Errorf("diagnostics %q do not name %q", summaries, tc.wantSub)
			}
		})
	}
}

func TestCredentialResourceMetadata(t *testing.T) {
	resp := resource.MetadataResponse{}
	(&credentialResource{}).Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_container_registry_credential" {
		t.Errorf("type name = %s", resp.TypeName)
	}
}
