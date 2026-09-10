package appgw_health_check

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// schemaOf returns this resource's schema, which every plan and state in these
// tests is built against.
func schemaOf(t *testing.T) tfsdk.Plan {
	t.Helper()
	r := NewResource()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatalf("schema: %v", sr.Diagnostics.Errors())
	}
	return tfsdk.Plan{Schema: sr.Schema}
}

func planOf(t *testing.T, m HealthCheckModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m HealthCheckModel) tfsdk.State {
	t.Helper()
	p := schemaOf(t)
	s := tfsdk.State{Schema: p.Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("state: %v", d.Errors())
	}
	return s
}

// emptyState is what Create writes into.
func emptyState(t *testing.T) tfsdk.State {
	t.Helper()
	return tfsdk.State{Schema: schemaOf(t).Schema}
}

// serve builds a client pointed at a handler, with the tenant pre-resolved.
func serve(t *testing.T, h http.HandlerFunc) (*client.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := client.NewClient(srv.URL, "test-key", client.WithHTTPClient(srv.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c, srv
}

// TestConfigureRejectsUnexpectedProviderData. A provider that hands the wrong
// type must be a clear error, not a nil-pointer panic on the first API call.
func TestConfigureRejectsUnexpectedProviderData(t *testing.T) {
	r := NewResource().(interface {
		Configure(context.Context, resource.ConfigureRequest, *resource.ConfigureResponse)
	})
	var resp resource.ConfigureResponse
	r.Configure(context.Background(), resource.ConfigureRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("a nil ProviderData is the not-yet-configured case and must be silent: %v",
			resp.Diagnostics.Errors())
	}
	r.Configure(context.Background(), resource.ConfigureRequest{ProviderData: 42}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("the wrong ProviderData type must be an error, not a later panic")
	}
}

var (
	_ = json.Marshal
	_ = types.StringValue
)

// hcPath is the health check's own endpoint. The pool's path is deliberately
// NOT kept alongside it any more: Delete used to probe the pool to decide which
// apology to print, and now calls the real delete route instead.
const hcPath = "/v1/tenants/t-1/application-gateways/agw-1/backend-pools/pool-1/health-check"

func hcFixture() apiHealthCheck {
	return apiHealthCheck{
		ID: "hc-1", PoolID: "pool-1", Protocol: "http", Path: "/healthz",
		ExpectedStatus: "200", IntervalSeconds: 10, TimeoutSeconds: 3,
		HealthyThreshold: 2, UnhealthyThreshold: 3,
	}
}

func hcModel() HealthCheckModel {
	return HealthCheckModel{
		GatewayID:       types.StringValue("agw-1"),
		PoolID:          types.StringValue("pool-1"),
		Protocol:        types.StringValue("http"),
		Path:            types.StringValue("/healthz"),
		IntervalSeconds: types.Int64Value(10),
		TimeoutSeconds:  types.Int64Value(3),
	}
}

// TestHealthCheckCreateIsAPut. Create and Update are the same call: the
// endpoint is keyed on the pool, so there is no first-write/later-write
// distinction.
func TestHealthCheckCreateReadUpdate(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == hcPath:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(hcFixture())
		case r.Method == http.MethodGet && r.URL.Path == hcPath:
			_ = json.NewEncoder(w).Encode(hcFixture())
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	hr := &healthCheckResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	hr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, hcModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created HealthCheckModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "hc-1" || created.GatewayID.ValueString() != "agw-1" {
		t.Fatalf("create result wrong: %+v", created)
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	hr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}

	updResp := resource.UpdateResponse{State: stateOf(t, created)}
	hr.Update(context.Background(), resource.UpdateRequest{Plan: planOf(t, created)}, &updResp)
	if updResp.Diagnostics.HasError() {
		t.Fatalf("update: %v", updResp.Diagnostics.Errors())
	}
}

// TestHealthCheckDeleteCallsTheRealEndpoint.
//
// 🔴 THIS RESOURCE USED TO APOLOGISE INSTEAD OF DELETING. There was no delete
// route, so Delete dropped the resource from state and warned that the check
// was still probing. `DELETE .../backend-pools/{poolId}/health-check` shipped;
// the assertion that matters is that the call is MADE -- a destroy that removes
// the resource from state without it is the provider reporting a change that
// did not happen, which is precisely what the old apology was.
func TestHealthCheckDeleteCallsTheRealEndpoint(t *testing.T) {
	var seen []string
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete && r.URL.Path == hcPath {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	hr := &healthCheckResource{client: c}
	m := hcModel()
	m.ID = types.StringValue("hc-1")
	resp := resource.DeleteResponse{State: stateOf(t, m)}
	hr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", resp.Diagnostics.Errors())
	}
	if len(seen) != 1 || seen[0] != "DELETE "+hcPath {
		t.Fatalf("requests = %v; the destroy must call DELETE %s, not drop the resource "+
			"from state and leave the check probing", seen, hcPath)
	}
	// The pool keeps running with no probe: every enabled backend now receives
	// traffic whether or not it answers. Said once, here.
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("removing the last probe from a live pool must be reported")
	}
}

// TestHealthCheckDeleteTreatsA404AsSuccess. The endpoint answers 404 when the
// pool has no check to remove -- which is the state a destroy is asking for.
// The same 404 arrives when the pool itself is already gone, the common case
// when both are destroyed in one run.
func TestHealthCheckDeleteTreatsA404AsSuccess(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		// The FLAT refusal envelope, which is what client.IsNotFound requires
		// and what appgw's respondError renders. A bare 404 with no code is an
		// unrouted path, and must NOT read as "the resource is gone".
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "no health check"})
	})
	hr := &healthCheckResource{client: c}
	m := hcModel()
	m.ID = types.StringValue("hc-1")
	resp := resource.DeleteResponse{State: stateOf(t, m)}
	hr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 is the desired state, not a failure: %v", resp.Diagnostics.Errors())
	}
}

// TestHealthCheckDeleteSurfacesARealFailure. Anything that is not a 404 leaves
// the check in place, and saying so is the difference between a failed destroy
// and a probe that keeps running with nothing in state to describe it.
func TestHealthCheckDeleteSurfacesARealFailure(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INTERNAL", "message": "boom"})
	})
	hr := &healthCheckResource{client: c}
	m := hcModel()
	m.ID = types.StringValue("hc-1")
	resp := resource.DeleteResponse{State: stateOf(t, m)}
	hr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, m)}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 500 must fail the destroy: the check is still probing")
	}
}

func TestHealthCheckValidateConfigRejectsATimeoutThatCannotFinish(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	m := hcModel()
	m.TimeoutSeconds = types.Int64Value(10)
	m.IntervalSeconds = types.Int64Value(10)
	p := planOf(t, m)
	var resp resource.ValidateConfigResponse
	r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
		Config: tfsdk.Config(p),
	}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a probe allowed as long as its own interval must be refused")
	}
}

// TestImportState pins the composite id format.
//
// An import id is the only interface a practitioner has for adopting an
// existing resource, and getting it wrong silently produces a resource with
// empty addressing that fails on its first refresh with an unrelated error.
// The malformed cases must be refused by name.
func TestImportState(t *testing.T) {
	r := NewResource().(resource.ResourceWithImportState)

	run := func(id string) (resource.ImportStateResponse, bool) {
		resp := resource.ImportStateResponse{State: importState(t)}
		r.ImportState(context.Background(), resource.ImportStateRequest{ID: id}, &resp)
		return resp, resp.Diagnostics.HasError()
	}

	for _, bad := range []string{"", "agw-1", "agw-1/", "/pool-1"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{pool_id}")
		}
	}

	resp, err := run("agw-1/pool-1")
	if err {
		t.Fatalf("a well-formed import id was refused: %v", resp.Diagnostics.Errors())
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("gateway_id"), &got); d.HasError() {
			t.Fatalf("read gateway_id: %v", d.Errors())
		}
		if got.ValueString() != "agw-1" {
			t.Errorf("gateway_id = %q, want agw-1", got.ValueString())
		}
	}
	{
		var got types.String
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("pool_id"), &got); d.HasError() {
			t.Fatalf("read pool_id: %v", d.Errors())
		}
		if got.ValueString() != "pool-1" {
			t.Errorf("pool_id = %q, want pool-1", got.ValueString())
		}
	}
}

// importState is the state Terraform hands ImportState: an object whose every
// attribute is NULL, not the zero tfsdk.State — writing an attribute into the
// latter fails, because there is no object to write into.
//
// Derived from the schema rather than hand-listed, so adding an attribute does
// not silently leave it out.
func importState(t *testing.T) tfsdk.State {
	t.Helper()
	s := schemaOf(t).Schema
	obj := s.Type().TerraformType(context.Background()).(tftypes.Object)
	attrs := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		attrs[name] = tftypes.NewValue(at, nil)
	}
	return tfsdk.State{Schema: s, Raw: tftypes.NewValue(obj, attrs)}
}

// TestHealthCheckReadPicksUpProxyProtocolDrift.
//
// 🔴 THE ROUND-TRIP ACCEPTANCE TESTS CANNOT SEE THIS ONE. Both put and Read
// call fromAPI on a model that ALREADY carries the value -- the plan's in put,
// state's in Read -- so a fromAPI that simply never assigns proxy_protocol
// leaves the value that was there and every apply still agrees with itself.
// Measured: deleting the assignment leaves TestAccHealthCheckProxyProtocolIsPlanStable
// green. What it breaks is REFRESH: a probe header turned off in the portal, or
// on, is never noticed, and `terraform plan` reports no change against a server
// that disagrees with state.
//
// So the assertion has to be the one thing the round trip never arranges -- a
// server answering something state does not already say.
func TestHealthCheckReadPicksUpProxyProtocolDrift(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inState  bool
		onServer bool
	}{
		{"turned on elsewhere", false, true},
		{"turned off elsewhere", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != hcPath {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				hc := hcFixture()
				hc.ProxyProtocol = tc.onServer
				_ = json.NewEncoder(w).Encode(hc)
			})
			hr := &healthCheckResource{client: c}

			m := hcModel()
			m.ID = types.StringValue("hc-1")
			m.ProxyProtocol = types.BoolValue(tc.inState)

			resp := resource.ReadResponse{State: stateOf(t, m)}
			hr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("read: %v", resp.Diagnostics.Errors())
			}
			var got HealthCheckModel
			resp.State.Get(context.Background(), &got)
			if got.ProxyProtocol.ValueBool() != tc.onServer {
				t.Fatalf("state kept %v; a refresh must report what the server holds (%v), or the "+
					"probe header can be changed outside Terraform and no plan ever says so",
					got.ProxyProtocol.ValueBool(), tc.onServer)
			}
		})
	}
}

// TestHealthCheckPutSendsProxyProtocol pins the WIRE side of the same
// attribute: the key spelling and that `false` is encoded by OMISSION, which is
// what the endpoint reads as false. A PUT states the whole check, so an omitted
// key is not "leave it as it was" and the two encodings mean one thing.
func TestHealthCheckPutSendsProxyProtocol(t *testing.T) {
	for _, tc := range []struct {
		name     string
		set      bool
		wantKey  bool
		wantTrue bool
	}{
		{"on sends the key", true, true, true},
		{"off omits the key", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]any
			c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != hcPath {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				hc := hcFixture()
				hc.ProxyProtocol = tc.set
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(hc)
			})
			hr := &healthCheckResource{client: c}

			m := hcModel()
			m.ProxyProtocol = types.BoolValue(tc.set)
			resp := resource.CreateResponse{State: emptyState(t)}
			hr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("create: %v", resp.Diagnostics.Errors())
			}

			v, present := body["proxyProtocol"]
			if present != tc.wantKey {
				t.Fatalf("proxyProtocol present=%v, want %v; body was %v", present, tc.wantKey, body)
			}
			if present && v != tc.wantTrue {
				t.Fatalf("proxyProtocol=%v, want %v", v, tc.wantTrue)
			}
		})
	}
}
