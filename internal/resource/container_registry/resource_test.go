package container_registry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/failclosed"
)

func newMeAndRegistryServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-123", "tenantId": "tenant-456"})
			return
		}
		handler(w, r)
	}))
}

func configuredRegistryResource(t *testing.T, serverURL string) *containerRegistryResource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure failed: %v", err)
	}
	return &containerRegistryResource{client: c}
}

func registrySchema(t *testing.T) schema.Schema {
	t.Helper()
	resp := resource.SchemaResponse{}
	(&containerRegistryResource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}

func registryTFType(t *testing.T) tftypes.Type {
	t.Helper()
	return registrySchema(t).Type().TerraformType(context.Background())
}

func registryState(t *testing.T) tftypes.Value {
	t.Helper()
	return tftypes.NewValue(registryTFType(t), map[string]tftypes.Value{
		"id":        tftypes.NewValue(tftypes.String, "tenant-456"),
		"enabled":   tftypes.NewValue(tftypes.Bool, true),
		"endpoint":  tftypes.NewValue(tftypes.String, "registry.sweden.frostmoln.cloud"),
		"namespace": tftypes.NewValue(tftypes.String, "t-abc"),
		// Null, not zero: prior state may predate these attributes, and null is
		// also what a registry with no reported cap holds.
		"storage_limit_bytes": tftypes.NewValue(tftypes.Number, nil),
		"storage_used_bytes":  tftypes.NewValue(tftypes.Number, nil),
	})
}

const enabledBody = `{"enabled":true,"endpoint":"registry.sweden.frostmoln.cloud","namespace":"t-abc"}`

// The opt-in must send NO body: the namespace is derived from the verified
// tenant id, and the route is the two-segment /registry/settings form the
// gateway's feature gate matches on.
func TestCreateOptsInWithNoBody(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody []byte
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, enabledBody)
	})
	defer server.Close()

	s := registrySchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(registryTFType(t), nil)}}

	configuredRegistryResource(t, server.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: registryState(t)}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("create failed: %v", resp.Diagnostics.Errors())
	}
	if want := "/v1/tenants/tenant-456/registry/settings"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if body := string(gotBody); body != "" && body != "null" {
		t.Errorf("body = %q, want none", body)
	}

	var got ContainerRegistryModel
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("state get: %v", d.Errors())
	}
	if got.ID.ValueString() != "tenant-456" {
		t.Errorf("id = %q, want the tenant id", got.ID.ValueString())
	}
	if got.Namespace.ValueString() != "t-abc" {
		t.Errorf("namespace = %q", got.Namespace.ValueString())
	}
}

// A tenant that opted in elsewhere — the portal, the fm CLI, another state — must
// be ADOPTED, not refused. The opt-in is idempotent by design, and failing would
// leave that tenant permanently unmanageable from Terraform.
func TestCreateAdoptsAnAlreadyEnabledRegistry(t *testing.T) {
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"conflict","message":"already enabled",
				"details":{"reason":"REGISTRY_ALREADY_ENABLED"}}`)
			return
		}
		_, _ = io.WriteString(w, enabledBody)
	})
	defer server.Close()

	s := registrySchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(registryTFType(t), nil)}}

	configuredRegistryResource(t, server.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: registryState(t)}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an already-enabled registry must be adopted: %v", resp.Diagnostics.Errors())
	}
	var got ContainerRegistryModel
	if d := resp.State.Get(context.Background(), &got); d.HasError() {
		t.Fatalf("state get: %v", d.Errors())
	}
	if !got.Enabled.ValueBool() || got.Namespace.ValueString() != "t-abc" {
		t.Errorf("adopted state = %+v, want the live registry read back", got)
	}
}

// A 409 that is NOT the already-enabled marker is a real failure. Adopting on any
// conflict would record a registry that may not exist.
func TestCreateDoesNotAdoptAnUnrelatedConflict(t *testing.T) {
	// The GET must SUCCEED and report no registry. If it 409'd too, this test
	// would pass under a resource that adopts every conflict — the adoption's
	// follow-up read would fail and raise an error for the wrong reason, and the
	// discriminator would be pinned by nothing.
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"enabled":false,"endpoint":"registry.sweden.frostmoln.cloud"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"conflict","message":"something else entirely"}`)
	})
	defer server.Close()

	s := registrySchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(registryTFType(t), nil)}}

	configuredRegistryResource(t, server.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: registryState(t)}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unrelated conflict must not be adopted as success")
	}
	if !resp.State.Raw.IsNull() {
		t.Error("state was written for a registry that was never created")
	}
}

// `enabled: false` is the ANSWER for a tenant with no registry — the route
// answers 200 either way — so that, not a 404, is what "gone" looks like here.
func TestReadTreatsDisabledAsGone(t *testing.T) {
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"enabled":false,"endpoint":"registry.sweden.frostmoln.cloud"}`)
	})
	defer server.Close()

	s := registrySchema(t)
	raw := registryState(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredRegistryResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("read failed: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a registry answering enabled:false must be dropped from state")
	}
}

// Destroy must not report a teardown that did not happen: there is no endpoint
// to call, so it removes the resource from state and WARNS.
func TestDeleteWarnsAndDeletesNothing(t *testing.T) {
	var called bool
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	defer server.Close()

	s := registrySchema(t)
	raw := registryState(t)
	resp := &resource.DeleteResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredRegistryResource(t, server.URL).Delete(context.Background(),
		resource.DeleteRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if called {
		t.Error("a request was issued; there is no teardown endpoint to call")
	}
	if resp.Diagnostics.HasError() {
		t.Fatalf("destroy must succeed in removing state: %v", resp.Diagnostics.Errors())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("destroy removed the registry from state silently; the registry still exists and still bills")
	}
}

// The fail-closed contract: a gateway misroute must never read as "your registry
// is gone" and destroy state.
func TestRegistryRead_GatewayNotRouted_KeepsState(t *testing.T) {
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		failclosed.GatewayNotRouted(w)
	})
	defer server.Close()

	s := registrySchema(t)
	raw := registryState(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredRegistryResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	failclosed.AssertReadKeepsState(t, "container_registry",
		resp.State.Raw.IsNull(), resp.Diagnostics.HasError())
}

func TestRegistryResourceMetadata(t *testing.T) {
	resp := resource.MetadataResponse{}
	(&containerRegistryResource{}).Metadata(context.Background(),
		resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_container_registry" {
		t.Errorf("type name = %s", resp.TypeName)
	}
}

// A create that comes back reporting no registry must NOT be recorded as a
// successful apply. Without this the example's image-prefix output renders as
// "<endpoint>/" and a dependent credential fails against a registry that is not
// there.
func TestCreateRefusesAResponseWithNoRegistry(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"disabled", `{"enabled":false,"endpoint":"registry.sweden.frostmoln.cloud"}`},
		{"no namespace", `{"enabled":true,"endpoint":"registry.sweden.frostmoln.cloud"}`},
		// The body did not say. A plain bool would decode this to false and the
		// resource would be silently removed on the next read.
		{"enabled absent", `{"endpoint":"registry.sweden.frostmoln.cloud"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newMeAndRegistryServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, tc.body)
			})
			defer server.Close()

			s := registrySchema(t)
			resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(registryTFType(t), nil)}}

			configuredRegistryResource(t, server.URL).Create(context.Background(),
				resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: registryState(t)}}, resp)

			if !resp.Diagnostics.HasError() {
				t.Fatal("expected a refusal")
			}
			if !resp.State.Raw.IsNull() {
				t.Error("state was written for a registry that was not confirmed")
			}
		})
	}
}

// An ADOPTED registry gets the same check: a 409 followed by a read reporting no
// registry is not a successful adoption.
func TestCreateRefusesAnAdoptionThatReadsBackEmpty(t *testing.T) {
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"conflict","message":"already enabled",
				"details":{"reason":"REGISTRY_ALREADY_ENABLED"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"enabled":false,"endpoint":"registry.sweden.frostmoln.cloud"}`)
	})
	defer server.Close()

	s := registrySchema(t)
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: s, Raw: tftypes.NewValue(registryTFType(t), nil)}}

	configuredRegistryResource(t, server.URL).Create(context.Background(),
		resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: registryState(t)}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("adopted a registry the follow-up read says does not exist")
	}
}

// A 200 that omits `enabled` must not read as a deletion — removing the resource
// is destructive to the plan, so an unreadable body fails loudly instead.
func TestReadRefusesABodyWithNoEnabledField(t *testing.T) {
	server := newMeAndRegistryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"endpoint":"registry.sweden.frostmoln.cloud","namespace":"t-abc"}`)
	})
	defer server.Close()

	s := registrySchema(t)
	raw := registryState(t)
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: s, Raw: raw}}

	configuredRegistryResource(t, server.URL).Read(context.Background(),
		resource.ReadRequest{State: tfsdk.State{Schema: s, Raw: raw}}, resp)

	if resp.State.Raw.IsNull() {
		t.Fatal("an unreadable body was treated as a deletion")
	}
	if !resp.Diagnostics.HasError() {
		t.Error("the unreadable body was accepted silently")
	}
}

// 🔴 A CAP THE API DID NOT REPORT MUST BE NULL, NEVER 0.
//
// The API omits the storage numbers while the registry is disabled and, for the
// limit, in the window where it has no cap to report. Terraform has a real null,
// so use it: a practitioner reading `storage_limit_bytes == 0` would conclude
// their registry may hold nothing, and any `coalesce` around it would silently
// treat "unknown" as "none".
func TestStorageAttrsDistinguishesUnreportedFromZero(t *testing.T) {
	for name, tc := range map[string]struct {
		in              apiRegistrySettings
		wantLimitNull   bool
		wantUsedNull    bool
		wantLimit, want int64
	}{
		"nothing reported": {
			in: apiRegistrySettings{}, wantLimitNull: true, wantUsedNull: true,
		},
		"cap and usage": {
			in:        apiRegistrySettings{StorageLimitBytes: 10 << 30, StorageUsedBytes: 3 << 30},
			wantLimit: 10 << 30, want: 3 << 30,
		},
		// Zero used against a real cap is a FACT, not an absence: a fresh registry
		// sits there, and reporting null would hide the cap the customer needs.
		"cap with zero usage": {
			in: apiRegistrySettings{StorageLimitBytes: 10 << 30}, wantLimit: 10 << 30, want: 0,
		},
		// The API CAN send this: registry reads the hard and used quota entries
		// independently, so a quota carrying usage and no hard limit yields exactly
		// this body. Usage with nothing to judge it against is reported as unknown
		// rather than as a bare number.
		"usage but no cap": {
			in: apiRegistrySettings{StorageUsedBytes: 5 << 20}, wantLimitNull: true, wantUsedNull: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			limit, used := storageAttrs(&tc.in)
			if limit.IsNull() != tc.wantLimitNull {
				t.Errorf("limit null = %v, want %v", limit.IsNull(), tc.wantLimitNull)
			}
			if used.IsNull() != tc.wantUsedNull {
				t.Errorf("used null = %v, want %v", used.IsNull(), tc.wantUsedNull)
			}
			if !tc.wantLimitNull && limit.ValueInt64() != tc.wantLimit {
				t.Errorf("limit = %d, want %d", limit.ValueInt64(), tc.wantLimit)
			}
			if !tc.wantUsedNull && used.ValueInt64() != tc.want {
				t.Errorf("used = %d, want %d", used.ValueInt64(), tc.want)
			}
		})
	}
}
