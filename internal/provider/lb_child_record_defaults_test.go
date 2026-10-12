package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags/tftagstest"
)

// recordedDefaultKeys decodes the default_tags keys a resource's private state
// says were applied at its last write (tftags' "tftags_default_keys").
func recordedDefaultKeys(t *testing.T, private []byte) []string {
	t.Helper()
	if len(private) == 0 {
		return nil
	}
	var merged map[string][]byte
	if err := json.Unmarshal(private, &merged); err != nil {
		t.Fatalf("private state is not the framework's map: %v", err)
	}
	var keys []string
	if raw := merged["tftags_default_keys"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatalf("recorded default keys: %v", err)
		}
	}
	slices.Sort(keys)
	return keys
}

// TestLBChildUpdateRecordsDefaultsOnlyAfterTheOperationSucceeds: an LB child's
// update is a provisioning operation. When that operation fails, Terraform keeps
// the prior state — and the private state returned with it must keep the prior
// default_tags keys too. Recording the new ones before the wait would tell the
// next plan that a default the platform never received is applied, and a later
// removal of it would be planned against a set the platform does not hold.
func TestLBChildUpdateRecordsDefaultsOnlyAfterTheOperationSucceeds(t *testing.T) {
	t.Parallel()
	for _, typeName := range []string{"frostmoln_lb_listener", "frostmoln_lb_member", "frostmoln_lb_pool", "frostmoln_lb_health_monitor"} {
		t.Run(typeName, func(t *testing.T) {
			t.Parallel()
			for _, fail := range []bool{true, false} {
				prof := tftagstest.Profiles[typeName]
				var failOps atomic.Bool
				prof.Handle = func(_ *tftagstest.Fake, w http.ResponseWriter, r *http.Request, _ map[string]json.RawMessage) bool {
					if !failOps.Load() || r.Method != http.MethodGet || r.URL.Path != "/v1/tenants/t-1/operations/op-1" {
						return false
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"operationId": "op-1", "status": "failed", "error": "refused by the test",
					})
					return true
				}

				var schema resource.SchemaResponse
				for _, nr := range New("test")().(*FrostmolnProvider).Resources(context.Background()) {
					r := nr()
					var md resource.MetadataResponse
					r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &md)
					if md.TypeName == typeName {
						r.Schema(context.Background(), resource.SchemaRequest{}, &schema)
					}
				}
				h := newTagHarness(t, typeName, schema.Schema, prof)

				srv1 := h.server(map[string]string{"env": "prod"})
				cfg := h.createConfig(map[string]string{"app": "a"})
				planned, plannedPriv, _ := h.plan(srv1, h.null(), cfg, nil)
				st, priv := h.apply(srv1, h.null(), planned, cfg, plannedPriv)
				if got := recordedDefaultKeys(t, priv); !slices.Equal(got, []string{"env"}) {
					t.Fatalf("after create: recorded defaults %v, want [env]", got)
				}

				srv2 := h.server(map[string]string{"env": "prod", "team": "web"})
				cfg2 := h.configFrom(st, map[string]string{"app": "a"})
				planned2, plannedPriv2, _ := h.plan(srv2, st, cfg2, priv)
				failOps.Store(fail)
				resp, err := srv2.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
					TypeName: typeName, PriorState: h.dyn(st), PlannedState: h.dyn(planned2),
					Config: h.dyn(cfg2), PlannedPrivate: plannedPriv2,
				})
				if err != nil {
					t.Fatalf("ApplyResourceChange: %v", err)
				}
				hasErr := false
				for _, d := range resp.Diagnostics {
					hasErr = hasErr || d.Severity == tfprotov6.DiagnosticSeverityError
				}
				got := recordedDefaultKeys(t, resp.Private)
				if fail {
					if !hasErr {
						t.Fatal("a failed update operation was reported as applied")
					}
					if !slices.Equal(got, []string{"env"}) {
						t.Errorf("failed update: recorded defaults %v, want the prior [env] kept", got)
					}
					continue
				}
				if hasErr {
					t.Fatalf("update: %v", resp.Diagnostics)
				}
				if !slices.Equal(got, []string{"env", "team"}) {
					t.Errorf("successful update: recorded defaults %v, want [env team]", got)
				}
			}
		})
	}
}

// lbChildListKeys is the key each LB child's collection listing answers under,
// which the create-timeout adoption sweep reads.
var lbChildListKeys = map[string]string{
	"frostmoln_lb_listener": "listeners",
	"frostmoln_lb_pool":     "pools",
	"frostmoln_lb_member":   "members",
}

// TestLBChildAdoptedCreateRecordsTheCreatesDefaults: a create whose operation
// ends without a verdict (cancelled — not a refusal) is resolved by the
// adoption sweep. The adopted object was created from the body that carried
// the create's default_tags, so exactly those keys are recorded with it.
func TestLBChildAdoptedCreateRecordsTheCreatesDefaults(t *testing.T) {
	t.Parallel()
	for _, typeName := range []string{"frostmoln_lb_listener", "frostmoln_lb_member", "frostmoln_lb_pool", "frostmoln_lb_health_monitor"} {
		t.Run(typeName, func(t *testing.T) {
			t.Parallel()
			prof := tftagstest.Profiles[typeName]
			listKey := lbChildListKeys[typeName]
			prof.Handle = func(f *tftagstest.Fake, w http.ResponseWriter, r *http.Request, _ map[string]json.RawMessage) bool {
				const tenant = "/v1/tenants/t-1"
				if r.Method != http.MethodGet {
					return false
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case tenant + "/operations/op-1":
					// No verdict: neither completed nor a refusal.
					_ = json.NewEncoder(w).Encode(map[string]any{"operationId": "op-1", "status": "cancelled"})
					return true
				case tenant + prof.Member, tenant + prof.Create:
					// The object the create made: stamped now, so the sweep's
					// created-at floor keeps it, and named as configured.
					obj := f.Object()
					obj["createdAt"] = time.Now().UTC().Format(time.RFC3339)
					obj["name"] = "x"
					if r.URL.Path == tenant+prof.Create && listKey != "" {
						_ = json.NewEncoder(w).Encode(map[string]any{listKey: []any{obj}})
						return true
					}
					if r.URL.Path == tenant+prof.Member {
						_ = json.NewEncoder(w).Encode(obj)
						return true
					}
				}
				return false
			}

			var schema resource.SchemaResponse
			for _, nr := range New("test")().(*FrostmolnProvider).Resources(context.Background()) {
				r := nr()
				var md resource.MetadataResponse
				r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &md)
				if md.TypeName == typeName {
					r.Schema(context.Background(), resource.SchemaRequest{}, &schema)
				}
			}
			h := newTagHarness(t, typeName, schema.Schema, prof)
			srv := h.server(map[string]string{"env": "prod", "team": "web"})
			cfg := h.createConfig(map[string]string{"app": "a"})
			planned, plannedPriv, _ := h.plan(srv, h.null(), cfg, nil)
			resp, err := srv.ApplyResourceChange(context.Background(), &tfprotov6.ApplyResourceChangeRequest{
				TypeName: typeName, PriorState: h.dyn(h.null()), PlannedState: h.dyn(planned),
				Config: h.dyn(cfg), PlannedPrivate: plannedPriv,
			})
			if err != nil {
				t.Fatalf("ApplyResourceChange: %v", err)
			}
			if st := h.decode(resp.NewState); st.IsNull() {
				t.Fatalf("the created object was not adopted: %v", resp.Diagnostics)
			}
			if got := recordedDefaultKeys(t, resp.Private); !slices.Equal(got, []string{"env", "team"}) {
				t.Errorf("adopted create: recorded defaults %v, want the create's [env team]", got)
			}
		})
	}
}
