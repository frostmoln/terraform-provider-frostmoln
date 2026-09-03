package application_gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

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

func planOf(t *testing.T, m GatewayModel) tfsdk.Plan {
	t.Helper()
	p := schemaOf(t)
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatalf("plan: %v", d.Errors())
	}
	return p
}

func stateOf(t *testing.T, m GatewayModel) tfsdk.State {
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

const gwBase = "/v1/tenants/t-1/application-gateways"

func gwFixture(status string) apiGateway {
	rev := int64(3)
	return apiGateway{
		ID: "agw-1", Name: "edge", TenantID: "t-1", Status: status,
		FlavorID: "agw.gp1.small", Version: "1.0.0", VPCID: "vpc-1", SubnetID: "sub-1",
		VPCCIDR: "10.0.0.0/16", PrivateIP: "10.0.0.5",
		PublicIPMode: "allocated", PublicIP: "203.0.113.10",
		ConfigGeneration: 3, ConfigRevision: &rev, ConfigStatus: "applied",
		CreatedAt: "2026-08-01T00:00:00Z",
	}
}

func gwModel() GatewayModel {
	return GatewayModel{
		Name:         types.StringValue("edge"),
		FlavorID:     types.StringValue("agw.gp1.small"),
		VPCID:        types.StringValue("vpc-1"),
		SubnetID:     types.StringValue("sub-1"),
		PublicIPMode: types.StringValue("allocated"),
	}
}

// TestGatewayCreateWritesStateBeforeWaiting.
//
// 🔴 THE GATEWAY EXISTS FROM THE 202 ONWARD. If the provisioning wait fails, or
// the run is interrupted, state must already name it — otherwise the
// practitioner is left with a real, billed gateway holding a Public IP that
// Terraform has never heard of and will never destroy.
func TestGatewayCreateWritesStateEvenWhenProvisioningFails(t *testing.T) {
	shrinkWaits(t)
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == gwBase:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(apiCreateGatewayResponse{
				Gateway: ptr(gwFixture("creating")), OperationID: "op-1",
			})
		default:
			// The operation lookup fails, standing in for provisioning falling
			// over after the row exists.
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "BOOM", "message": "no"})
		}
	})
	gr := &gatewayResource{client: c}

	resp := resource.CreateResponse{State: emptyState(t)}
	gr.Create(context.Background(), resource.CreateRequest{Plan: planOf(t, gwModel())}, &resp)

	if resp.State.Raw.IsNull() {
		t.Fatal("the gateway was created and then dropped from state: it is billed, holds an " +
			"address, and Terraform will never destroy it")
	}
	var created GatewayModel
	resp.State.Get(context.Background(), &created)
	if created.ID.ValueString() != "agw-1" {
		t.Fatalf("state does not name the created gateway: %+v", created)
	}
}

func TestGatewayReadUpdateDelete(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == gwBase+"/agw-1":
			_ = json.NewEncoder(w).Encode(gwFixture("running"))
		case r.Method == http.MethodPatch && r.URL.Path == gwBase+"/agw-1":
			g := gwFixture("running")
			g.Name = "renamed"
			_ = json.NewEncoder(w).Encode(g)
		case r.Method == http.MethodDelete && r.URL.Path == gwBase+"/agw-1":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/events"):
			// The client waits on the tenant SSE stream instead of a timer
			// (internal/client/events.go). A 404 stands in for a gateway that does
			// not serve it -- an explicitly supported degradation back to timer
			// polling -- so this mock stays hermetic and exercises that path.
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	gr := &gatewayResource{client: c}
	m := gwModel()
	m.ID = types.StringValue("agw-1")

	readResp := resource.ReadResponse{State: stateOf(t, m)}
	gr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &readResp)
	if readResp.Diagnostics.HasError() || readResp.State.Raw.IsNull() {
		t.Fatalf("read: %v", readResp.Diagnostics.Errors())
	}

	plan := m
	plan.Name = types.StringValue("renamed")
	updResp := resource.UpdateResponse{State: stateOf(t, m)}
	gr.Update(context.Background(), resource.UpdateRequest{
		Plan: planOf(t, plan), State: stateOf(t, m),
	}, &updResp)
	if updResp.Diagnostics.HasError() {
		t.Fatalf("update: %v", updResp.Diagnostics.Errors())
	}

	delResp := resource.DeleteResponse{State: stateOf(t, m)}
	gr.Delete(context.Background(), resource.DeleteRequest{State: stateOf(t, m)}, &delResp)
	if delResp.Diagnostics.HasError() {
		t.Fatalf("delete: %v", delResp.Diagnostics.Errors())
	}
}

// TestGatewayReadDropsASoftDeletedGateway. A deleted gateway reads back with
// status "deleted"; keeping it would leave a tombstone in state that never
// converges.
func TestGatewayReadDropsASoftDeletedGateway(t *testing.T) {
	c, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(gwFixture("deleted"))
	})
	gr := &gatewayResource{client: c}
	m := gwModel()
	m.ID = types.StringValue("agw-1")
	resp := resource.ReadResponse{State: stateOf(t, m)}
	gr.Read(context.Background(), resource.ReadRequest{State: stateOf(t, m)}, &resp)
	if !resp.State.Raw.IsNull() {
		t.Fatal("a gateway reading back as deleted must be removed from state")
	}
}

// TestGatewayValidateConfigRefusesInconsistentPublicIPFlags. Getting this pair
// wrong is how a practitioner either loses an address they own or is billed for
// one they did not ask for.
func TestGatewayValidateConfigRefusesInconsistentPublicIPFlags(t *testing.T) {
	r := NewResource().(resource.ResourceWithValidateConfig)
	check := func(m GatewayModel) bool {
		p := planOf(t, m)
		var resp resource.ValidateConfigResponse
		r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
			Config: tfsdk.Config(p),
		}, &resp)
		return resp.Diagnostics.HasError()
	}

	if check(gwModel()) {
		t.Error("a plain pool-allocated gateway was refused")
	}

	allocatedWithID := gwModel()
	allocatedWithID.PublicIPID = types.StringValue("pip-1")
	if !check(allocatedWithID) {
		t.Error("naming an address while asking the pool to allocate one must be refused")
	}

	selectedNoID := gwModel()
	selectedNoID.PublicIPMode = types.StringValue("selected")
	if !check(selectedNoID) {
		t.Error("public_ip_mode = selected with no address must be refused")
	}

	selectedOK := gwModel()
	selectedOK.PublicIPMode = types.StringValue("selected")
	selectedOK.PublicIPID = types.StringValue("pip-1")
	if check(selectedOK) {
		t.Error("a valid bring-your-own gateway was refused")
	}
}

func ptr[T any](v T) *T { return &v }

// shrinkWaits makes a provisioning wait finish in milliseconds. Without it a
// test that exercises a FAILING wait runs for the real twenty-minute timeout.
func shrinkWaits(t *testing.T) {
	t.Helper()
	oc, od, op := createTimeout, deleteTimeout, pollInterval
	createTimeout, deleteTimeout, pollInterval = 200*time.Millisecond, 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { createTimeout, deleteTimeout, pollInterval = oc, od, op })
}
