package appgw_listener

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	tfpath "github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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

func planOf(t *testing.T, m ListenerModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m ListenerModel) tfsdk.State {
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

const lsnBase = "/v1/tenants/t-1/application-gateways/agw-1/listeners"

func lsnFixture() apiListener {
	return apiListener{
		ID: "lsn-1", GatewayID: "agw-1", Name: "https", Protocol: "https", Port: 443,
		TLSMinVersion: "1.2", TLSCipherProfile: "modern", GeoBlockMode: "off",
		Enabled: true, CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func lsnModel() ListenerModel {
	// The list fields need an explicit null-with-element-type: a zero
	// types.List carries no element type and cannot be written into a schema
	// that declares one.
	nullList := types.ListNull(types.StringType)
	return ListenerModel{
		GatewayID:         types.StringValue("agw-1"),
		Name:              types.StringValue("https"),
		Protocol:          types.StringValue("https"),
		Port:              types.Int64Value(443),
		SNICertificateIDs: nullList,
		AllowedCIDRs:      nullList,
		DeniedCIDRs:       nullList,
		GeoCountries:      nullList,
	}
}

func TestListenerCreateReadDelete(t *testing.T) {
	var seen []string
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == lsnBase:
			// The server answers 202 with the LISTENER, not an operation
			// envelope: the row is written synchronously and 202 says only that
			// it is not yet serving.
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(lsnFixture())
		case r.Method == http.MethodGet && r.URL.Path == lsnBase+"/lsn-1":
			_ = json.NewEncoder(w).Encode(lsnFixture())
		case r.Method == http.MethodDelete && r.URL.Path == lsnBase+"/lsn-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	r := &listenerResource{client: c}

	createResp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, lsnModel())}, &createResp)
	if createResp.Diagnostics.HasError() {
		t.Fatalf("create: %v", createResp.Diagnostics.Errors())
	}
	var created ListenerModel
	createResp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "lsn-1" {
		t.Fatalf("id = %q, want lsn-1", created.ID.ValueString())
	}

	readResp := resource.ReadResponse{State: stateOf(t, created)}
	r.Read(context.Background(), resource.ReadRequest{State: stateOf(t, created)}, &readResp)
	if readResp.Diagnostics.HasError() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}

	delResp := resource.DeleteResponse{State: stateOf(t, created)}
	r.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, created)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

// TestListenerReadRemovesAVanishedResource. A 404 on refresh means it was
// deleted outside Terraform; erroring instead would wedge every subsequent plan.
func TestListenerReadRemovesAVanishedResource(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": "gone"})
	})
	r := &listenerResource{client: c}
	m := lsnModel()
	m.ID = types.StringValue("lsn-1")

	resp := resource.ReadResponse{State: stateOf(t, m)}
	r.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a 404 must not be an error: %v", resp.Diagnostics.Errors())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a vanished listener must be removed from state")
	}
}

// TestListenerUpdateIsRefused. Every attribute carries RequiresReplace because
// the API has no update, so Update is unreachable — and must SAY so rather than
// silently succeed, which is how a provider reports a change that never
// happened.
func TestListenerUpdateIsRefused(t *testing.T) {
	r := &listenerResource{}
	var resp resource.UpdateResponse
	r.Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must refuse: a silent no-op would report a change that did not happen")
	}
}

func TestListenerCreateSurfacesAnAPIError(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "INVALID_REQUEST", "message": "no"})
	})
	r := &listenerResource{client: c}
	resp := resource.CreateResponse{State: emptyState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, lsnModel())}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 400 must surface as an error")
	}
}

// TestListenerValidateConfig pins the two cross-field rules the server enforces,
// caught here so a mistake is a plan-time error rather than a 400 partway
// through an apply that has already built the gateway.
func TestListenerValidateConfig(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m ListenerModel) []string {
		// tfsdk.Config has no Set; build the raw value through a Plan (which
		// does) and carry it across with the same schema.
		p := planOf(t, m)
		cfg := tfsdk.Config(p)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{Config: cfg}, &resp)
		var out []string
		for _, e := range resp.Diagnostics.Errors() {
			out = append(out, e.Summary())
		}
		return out
	}

	ok := lsnModel()
	if errs := check(ok); len(errs) != 0 {
		t.Fatalf("a valid https listener was refused: %v", errs)
	}

	// A certificate on an http listener would never be served.
	httpWithCert := lsnModel()
	httpWithCert.Protocol = types.StringValue("http")
	httpWithCert.Port = types.Int64Value(80)
	httpWithCert.DefaultCertificateID = types.StringValue("cert-1")
	if errs := check(httpWithCert); len(errs) == 0 {
		t.Error("a certificate on an http listener must be refused")
	}

	// Geo filtering with no countries would apply to everything or nothing.
	geoNoCountries := lsnModel()
	geoNoCountries.GeoBlockMode = types.StringValue("deny")
	if errs := check(geoNoCountries); len(errs) == 0 {
		t.Error("geo_block_mode = deny with no countries must be refused")
	}
}

// tcpModel is a valid single-port tcp listener: the shape every case below
// perturbs by exactly one field.
func tcpModel() ListenerModel {
	m := lsnModel()
	m.Name = types.StringValue("smtp")
	m.Protocol = types.StringValue("tcp")
	m.Port = types.Int64Value(25)
	m.BackendPoolID = types.StringValue("pool-1")
	return m
}

// TestListenerValidateConfigProtocolShape pins the http/https-versus-tcp split.
//
// 🔴 EVERY CASE HERE IS A 400 THE PRACTITIONER WOULD OTHERWISE MEET MID-APPLY,
// and on this resource that is worse than usual: creating a listener OPENS A
// PUBLIC PORT immediately rather than on the next configuration apply, so an
// apply that gets three listeners in and fails on the fourth has already
// changed what the internet can reach.
//
// Both directions of each rule, because a guard that refuses only the surplus
// field lets the missing one straight through: `tcp` REQUIRES backend_pool_id
// and http/https REFUSE it, and the same inversion holds for port_range_end.
func TestListenerValidateConfigProtocolShape(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m ListenerModel) []string {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(),
			resource.ValidateConfigRequest{Config: tfsdk.Config(p)}, &resp)
		var out []string
		for _, e := range resp.Diagnostics.Errors() {
			out = append(out, e.Summary())
		}
		return out
	}

	// The valid shapes first, so a guard that refuses everything cannot pass.
	if errs := check(tcpModel()); len(errs) != 0 {
		t.Fatalf("a valid single-port tcp listener was refused: %v", errs)
	}
	ranged := tcpModel()
	ranged.Port = types.Int64Value(8000)
	ranged.PortRangeEnd = types.Int64Value(8100)
	if errs := check(ranged); len(errs) != 0 {
		t.Fatalf("a valid ranged tcp listener was refused: %v", errs)
	}
	if errs := check(lsnModel()); len(errs) != 0 {
		t.Fatalf("a valid https listener was refused: %v", errs)
	}

	for name, tc := range map[string]struct {
		m    ListenerModel
		want string
	}{
		"a single-port listener on the appliance's own port": {
			m: func() ListenerModel {
				m := tcpModel()
				m.Port = types.Int64Value(9000)
				return m
			}(),
			want: "Port 9000 Is Reserved By The Gateway Appliance",
		},
		"a range that swallows the appliance's own port": {
			m: func() ListenerModel {
				m := tcpModel()
				m.Port = types.Int64Value(8990)
				m.PortRangeEnd = types.Int64Value(9010)
				return m
			}(),
			want: "Port 9000 Is Reserved By The Gateway Appliance",
		},
		"the reserved port is refused on an L7 listener too": {
			m: func() ListenerModel {
				m := lsnModel()
				m.Port = types.Int64Value(9000)
				return m
			}(),
			want: "Port 9000 Is Reserved By The Gateway Appliance",
		},
		"tcp without a pool has nothing to forward to": {
			m:    func() ListenerModel { m := tcpModel(); m.BackendPoolID = types.StringNull(); return m }(),
			want: "backend_pool_id Is Required With protocol = \"tcp\"",
		},
		"https with a pool: the ROUTES name the pool": {
			m: func() ListenerModel {
				m := lsnModel()
				m.BackendPoolID = types.StringValue("pool-1")
				return m
			}(),
			want: "backend_pool_id Requires protocol = \"tcp\"",
		},
		"an https listener binds exactly one port": {
			m: func() ListenerModel {
				m := lsnModel()
				m.PortRangeEnd = types.Int64Value(8100)
				return m
			}(),
			want: "port_range_end Requires protocol = \"tcp\"",
		},
		"a range must be above its start, not equal to it": {
			m: func() ListenerModel {
				m := tcpModel()
				m.Port = types.Int64Value(8000)
				m.PortRangeEnd = types.Int64Value(8000)
				return m
			}(),
			want: "port_range_end Must Be Greater Than port",
		},
		"an inverted range": {
			m: func() ListenerModel {
				m := tcpModel()
				m.Port = types.Int64Value(8100)
				m.PortRangeEnd = types.Int64Value(8000)
				return m
			}(),
			want: "port_range_end Must Be Greater Than port",
		},
		// One socket per port in the range, so the span is a tenant-settable
		// multiplier on the appliance's descriptor budget.
		"a span above the cap": {
			m: func() ListenerModel {
				m := tcpModel()
				m.Port = types.Int64Value(1000)
				m.PortRangeEnd = types.Int64Value(1000 + maxPortRangeSpan)
				return m
			}(),
			want: "port_range_end Spans Too Many Ports",
		},
		"tcp terminates no TLS, so a minimum version is never negotiated": {
			m: func() ListenerModel {
				m := tcpModel()
				m.TLSMinVersion = types.StringValue("1.3")
				return m
			}(),
			want: "tls_min_version Requires an https Listener",
		},
		"tcp terminates no TLS, so a cipher profile is never chosen": {
			m: func() ListenerModel {
				m := tcpModel()
				m.TLSCipherProfile = types.StringValue("modern")
				return m
			}(),
			want: "tls_cipher_profile Requires an https Listener",
		},
		"a redirect is an HTTP response and tcp sends none": {
			m: func() ListenerModel {
				m := tcpModel()
				m.RedirectToHTTPS = types.BoolValue(true)
				return m
			}(),
			want: "redirect_to_https Requires an http Listener",
		},
		"a certificate on a tcp listener would never be served": {
			m: func() ListenerModel {
				m := tcpModel()
				m.DefaultCertificateID = types.StringValue("cert-1")
				return m
			}(),
			want: "Certificates Require protocol = \"https\"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			errs := check(tc.m)
			for _, e := range errs {
				if e == tc.want {
					return
				}
			}
			t.Errorf("got %v, want an error %q", errs, tc.want)
		})
	}

	// The largest LEGAL span, so the cap is off-by-one-proof in both
	// directions: exactly maxPortRangeSpan ports must be accepted.
	atCap := tcpModel()
	atCap.Port = types.Int64Value(1000)
	atCap.PortRangeEnd = types.Int64Value(1000 + maxPortRangeSpan - 1)
	if errs := check(atCap); len(errs) != 0 {
		t.Errorf("a span of exactly %d ports was refused: %v", maxPortRangeSpan, errs)
	}
}

// TestListenerCreateSendsTheTCPShape. The two new fields have to reach the wire
// under the names the server reads, and -- just as importantly -- must leave NO
// key behind when unset: the server REFUSES either on an http/https listener
// rather than ignoring it, so an empty-but-present key is a 400.
func TestListenerCreateSendsTheTCPShape(t *testing.T) {
	capture := func(m ListenerModel) map[string]any {
		var body map[string]any
		c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(lsnFixture())
		})
		lr := &listenerResource{client: c}
		resp := resource.CreateResponse{State: emptyState(t)}
		lr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, m)}, &resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("create: %v", resp.Diagnostics.Errors())
		}
		return body
	}

	ranged := tcpModel()
	ranged.Port = types.Int64Value(8000)
	ranged.PortRangeEnd = types.Int64Value(8100)
	body := capture(ranged)
	if v, ok := body["portRangeEnd"].(float64); !ok || int(v) != 8100 {
		t.Errorf("portRangeEnd = %v, want 8100", body["portRangeEnd"])
	}
	if body["backendPoolId"] != "pool-1" {
		t.Errorf("backendPoolId = %v, want pool-1", body["backendPoolId"])
	}

	body = capture(lsnModel())
	if _, present := body["portRangeEnd"]; present {
		t.Errorf("portRangeEnd was sent on an https listener (%v); the server refuses the key "+
			"rather than ignoring it", body["portRangeEnd"])
	}
	if _, present := body["backendPoolId"]; present {
		t.Errorf("backendPoolId was sent on an https listener (%v); the server refuses the key "+
			"rather than ignoring it", body["backendPoolId"])
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

	for _, bad := range []string{"", "agw-1", "agw-1/", "/lsn-1", "a/b/c"} {
		if _, err := run(bad); !err {
			t.Errorf("import id %q was accepted; the format is %s", bad, "{gateway_id}/{listener_id}")
		}
	}

	resp, err := run("agw-1/lsn-1")
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
		if d := resp.State.GetAttribute(context.Background(), tfpath.Root("id"), &got); d.HasError() {
			t.Fatalf("read id: %v", d.Errors())
		}
		if got.ValueString() != "lsn-1" {
			t.Errorf("id = %q, want lsn-1", got.ValueString())
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

// 🔴 max_connections HAS ITS OWN CEILING, LOWER THAN ITS TWO NEIGHBOURS'.
//
// rate_limit_rps and rate_limit_burst are bounded at 1_000_000 server-side;
// max_connections is bounded at domain.MaxConnectionCeiling (200000), and the
// renderer clamps above it regardless. The 1_000_000 literal was reused for all
// three, so 200001..1000000 passed plan and 400'd at apply.
//
// Asserted from BOTH sides: the ceiling itself is accepted, one above it is
// refused. A one-sided assertion passes against a validator that refuses
// everything.
func TestListenerMaxConnectionsCeilingMatchesTheServer(t *testing.T) {
	r := NewResource()
	var sr resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &sr)

	attr, ok := sr.Schema.Attributes["max_connections"].(schema.Int64Attribute)
	if !ok {
		t.Fatalf("max_connections is not an Int64Attribute; got %T", sr.Schema.Attributes["max_connections"])
	}
	if len(attr.Validators) == 0 {
		t.Fatal("max_connections has no validators; any value would reach the server")
	}

	run := func(v int64) []string {
		var out []string
		for _, val := range attr.Validators {
			var resp validator.Int64Response
			val.ValidateInt64(context.Background(), validator.Int64Request{
				Path:        tfpath.Root("max_connections"),
				ConfigValue: types.Int64Value(v),
			}, &resp)
			for _, e := range resp.Diagnostics.Errors() {
				out = append(out, e.Summary())
			}
		}
		return out
	}

	if errs := run(maxConnectionCeiling); len(errs) != 0 {
		t.Errorf("the ceiling itself (%d) was refused: %v", maxConnectionCeiling, errs)
	}
	if errs := run(maxConnectionCeiling + 1); len(errs) == 0 {
		t.Errorf("max_connections = %d passed plan; the server refuses anything above %d, so "+
			"this reaches apply as a 400 instead of a plan error",
			maxConnectionCeiling+1, maxConnectionCeiling)
	}
}
