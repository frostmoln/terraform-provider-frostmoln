package messaging_instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §8 (Phase 4a): the instance's TLS state is
// read back as computed attributes, and a create refused for a taken DNS name is an
// attribute error on `name`.

func TestFromAPI_TLSFields(t *testing.T) {
	var m MessagingInstanceModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiMessagingInstance{
		ID: "mq-1", TLSEnabled: true,
		Hostname:   "orders.mq.prod.omkcmw.ck4to7f6.fm.internal",
		PublicName: "orders.omkcmw.ck4to7f6.mq.svc.frostmoln.cloud",
	}, &diags)
	if diags.HasError() {
		t.Fatalf("fromAPI: %v", diags)
	}
	if !m.TLSEnabled.ValueBool() || m.Hostname.ValueString() != "orders.mq.prod.omkcmw.ck4to7f6.fm.internal" ||
		m.PublicName.ValueString() != "orders.omkcmw.ck4to7f6.mq.svc.frostmoln.cloud" {
		t.Errorf("TLS fields not read back: tls_enabled=%v hostname=%v public_name=%v", m.TLSEnabled, m.Hostname, m.PublicName)
	}
}

// A pre-TLS instance (or a service that predates the fields): tls_enabled false, and the
// names null, never "".
func TestFromAPI_PreTLS(t *testing.T) {
	var m MessagingInstanceModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiMessagingInstance{ID: "mq-1"}, &diags)
	if m.TLSEnabled.IsNull() || m.TLSEnabled.ValueBool() {
		t.Errorf("tls_enabled = %v, want false", m.TLSEnabled)
	}
	if !m.Hostname.IsNull() || !m.PublicName.IsNull() {
		t.Errorf("hostname / public_name must be null, got %v / %v", m.Hostname, m.PublicName)
	}
}

func TestSchema_TLSAttributesAreComputedOnly(t *testing.T) {
	r := NewResource()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	for _, name := range []string{"tls_enabled", "hostname", "public_name"} {
		a, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("attribute %q missing", name)
		}
		if !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("%s must be computed only", name)
		}
	}
	if _, ok := resp.Schema.Attributes["tls_not_after"]; ok {
		t.Error("tls_not_after must not exist: the messaging API does not send an expiry")
	}
}

func TestCreateDNSNameTakenIsAnAttributeErrorOnName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/messaging" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": "INSTANCE_DNS_NAME_TAKEN", "message": "another instance in this VPC holds the DNS label orders",
			})
			return
		}
		t.Errorf("unexpected request: %s %s (nothing may follow the refusal)", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := newTestMessagingResource(c)

	plan := buildMessagingInstancePlan(t, MessagingInstanceModel{
		Name:            types.StringValue("orders"),
		Type:            types.StringValue("lavinmq"),
		Version:         types.StringValue("2.3"),
		FlavorID:        types.StringValue("mq.gp1.small"),
		VPCID:           types.StringValue("vpc-1"),
		SubnetID:        types.StringValue("sn-1"),
		PersistenceMode: types.StringValue("persistent"),
	})
	createResp := resource.CreateResponse{State: emptyMessagingInstanceState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	errs := createResp.Diagnostics.Errors()
	if len(errs) != 1 {
		t.Fatalf("want exactly one error, got %v", errs)
	}
	withPath, ok := errs[0].(diag.DiagnosticWithPath)
	if !ok || !withPath.Path().Equal(path.Root("name")) {
		t.Fatalf("the refusal must be an attribute error on name, got %#v", errs[0])
	}
	if !strings.Contains(errs[0].Detail(), "Choose another `name`") || !strings.Contains(errs[0].Detail(), "INSTANCE_DNS_NAME_TAKEN") {
		t.Errorf("detail lacks the remedy or the platform's answer: %s", errs[0].Detail())
	}
}
