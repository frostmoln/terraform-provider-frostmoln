package vpc

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// Internal DNS (MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN Phase 2, D10/D12):
// dns_label (Optional+Computed, frozen, never replaces), dns_domain (Computed)
// and enable_dns_hostnames (Optional+Computed, the platform defaults it on).

// vpcValue builds a full frostmoln_vpc object value of an EXISTING VPC with
// the given overrides.
func vpcValue(t *testing.T, tfType tftypes.Type, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	vals := map[string]tftypes.Value{
		"id":                   tftypes.NewValue(tftypes.String, "vpc-1"),
		"name":                 tftypes.NewValue(tftypes.String, "prod"),
		"description":          tftypes.NewValue(tftypes.String, nil),
		"cidr":                 tftypes.NewValue(tftypes.String, "10.0.0.0/16"),
		"tags":                 tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"tags_all":             tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"status":               tftypes.NewValue(tftypes.String, "active"),
		"is_default":           tftypes.NewValue(tftypes.Bool, false),
		"subnet_count":         tftypes.NewValue(tftypes.Number, big.NewFloat(0)),
		"created_at":           tftypes.NewValue(tftypes.String, "2026-10-20T00:00:00Z"),
		"updated_at":           tftypes.NewValue(tftypes.String, nil),
		"dns_label":            tftypes.NewValue(tftypes.String, "prod"),
		"dns_domain":           tftypes.NewValue(tftypes.String, "prod.ck4to7.omkcmwdd.fm.internal."),
		"enable_dns_hostnames": tftypes.NewValue(tftypes.Bool, true),
		"timeouts":             tftypes.NewValue(tfType.(tftypes.Object).AttributeTypes["timeouts"], nil),
	}
	for k, v := range overrides {
		vals[k] = v
	}
	return tftypes.NewValue(tfType, vals)
}

func TestToCreateRequest_DNSFieldsOnlyWhenConfigured(t *testing.T) {
	m := VPCModel{
		Name: types.StringValue("prod"), CIDR: types.StringValue("10.0.0.0/16"),
		DNSLabel: types.StringUnknown(), EnableDNSHostnames: types.BoolUnknown(),
	}
	body, _ := json.Marshal(m.toCreateRequest())
	for _, k := range []string{"dnsLabel", "enableDnsHostnames"} {
		if strings.Contains(string(body), k) {
			t.Errorf("unconfigured %s was sent: %s", k, body)
		}
	}

	m.DNSLabel = types.StringValue("prod-net")
	m.EnableDNSHostnames = types.BoolValue(false)
	req := m.toCreateRequest()
	if req.DNSLabel != "prod-net" || req.EnableDNSHostnames == nil || *req.EnableDNSHostnames {
		t.Errorf("configured DNS fields not sent: %+v", req)
	}
}

func TestToUpdateRequest_HostnamesFromConfigAndNeverALabel(t *testing.T) {
	m := VPCModel{Name: types.StringValue("prod"), DNSLabel: types.StringValue("prod")}

	body, _ := json.Marshal(m.toUpdateRequest(types.BoolNull()))
	if strings.Contains(string(body), "enableDnsHostnames") {
		t.Errorf("an unconfigured enable_dns_hostnames was echoed: %s", body)
	}
	if strings.Contains(string(body), "dnsLabel") {
		t.Errorf("an update must never carry dnsLabel: %s", body)
	}

	body, _ = json.Marshal(m.toUpdateRequest(types.BoolValue(false)))
	if !strings.Contains(string(body), `"enableDnsHostnames":false`) {
		t.Errorf("a configured false must be sent: %s", body)
	}
}

func TestFromAPI_DNSFields(t *testing.T) {
	enabled := false
	var m VPCModel
	var diags diag.Diagnostics
	m.Tags = types.MapNull(types.StringType)
	m.fromAPI(context.Background(), &apiVPC{
		ID: "vpc-1", DNSLabel: "malmo-vast", DNSDomain: "malmo-vast.ck4to7.omkcmwdd.fm.internal.",
		EnableDNSHostnames: &enabled,
	}, &diags)
	if m.DNSLabel.ValueString() != "malmo-vast" || m.DNSDomain.ValueString() != "malmo-vast.ck4to7.omkcmwdd.fm.internal." ||
		m.EnableDNSHostnames.ValueBool() {
		t.Errorf("got %v / %v / %v", m.DNSLabel, m.DNSDomain, m.EnableDNSHostnames)
	}

	// Unassigned: null, never "" (a pinned "" would fail the apply that sees
	// the assignment as an inconsistent result).
	m.EnableDNSHostnames = types.BoolUnknown()
	m.fromAPI(context.Background(), &apiVPC{ID: "vpc-1"}, &diags)
	if !m.DNSLabel.IsNull() || !m.DNSDomain.IsNull() {
		t.Errorf("unassigned label/domain must be null, got %v / %v", m.DNSLabel, m.DNSDomain)
	}
	if m.EnableDNSHostnames.IsUnknown() {
		t.Error("enable_dns_hostnames must be known after a read")
	}
}

func TestDNSLabelChange(t *testing.T) {
	cases := []struct {
		name             string
		planned, current types.String
		refused          bool
		want             string
	}{
		{"unset", types.StringNull(), types.StringValue("prod"), false, ""},
		{"unknown", types.StringUnknown(), types.StringValue("prod"), false, ""},
		{"same", types.StringValue("prod"), types.StringValue("prod"), false, ""},
		{"changed", types.StringValue("other"), types.StringValue("prod"), true, "Cannot Change"},
		{"set on an unlabelled VPC", types.StringValue("prod"), types.StringNull(), true, "Existing VPC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary, _, refused := dnsLabelChange(tc.planned, tc.current)
			if refused != tc.refused || !strings.Contains(summary, tc.want) {
				t.Errorf("refused=%v summary=%q, want %v / %q", refused, summary, tc.refused, tc.want)
			}
		})
	}
}

// A dns_label change plans as a WARNING (never an error: that would block
// `terraform destroy` too) and never as a replacement.
func TestModifyPlan_DNSLabelChangeWarnsAndNeverReplaces(t *testing.T) {
	r := &vpcResource{client: client.NewClient("http://127.0.0.1:1", "k")} // pragma: allowlist secret
	schemaResp := getVPCSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	state := vpcValue(t, tfType, nil)
	changed := vpcValue(t, tfType, map[string]tftypes.Value{"dns_label": tftypes.NewValue(tftypes.String, "other")})

	resp := &resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: changed}}
	r.ModifyPlan(ctx, resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: changed},
		Plan:   tfsdk.Plan{Schema: schemaResp.Schema, Raw: changed},
		State:  tfsdk.State{Schema: schemaResp.Schema, Raw: state},
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("a plan-time dns_label refusal must be a warning, got errors: %v", resp.Diagnostics.Errors())
	}
	if len(resp.Diagnostics.Warnings()) != 1 || !strings.Contains(resp.Diagnostics.Warnings()[0].Detail(), "This apply will fail") {
		t.Errorf("expected one apply-will-fail warning, got %v", resp.Diagnostics.Warnings())
	}
	if len(resp.RequiresReplace) != 0 {
		t.Errorf("a dns_label change must never replace the VPC, got %v", resp.RequiresReplace)
	}
}

// Update refuses a changed dns_label BEFORE it sends anything, and an update
// that leaves enable_dns_hostnames unset never echoes it.
func TestUpdate_RefusesALabelChangeAndSendsHostnamesOnlyWhenConfigured(t *testing.T) {
	var mu sync.Mutex
	var puts []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "u", "tenantId": "t-1"})
		case "/v1/tenants/t-1/vpcs/vpc-1":
			if r.Method == http.MethodPut {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				puts = append(puts, body)
				mu.Unlock()
			}
			enabled := false
			_ = json.NewEncoder(w).Encode(apiVPC{
				ID: "vpc-1", Name: "prod", CIDR: "10.0.0.0/16", Status: "active",
				CreatedAt: "2026-10-20T00:00:00Z", DNSLabel: "prod",
				DNSDomain: "prod.ck4to7.omkcmwdd.fm.internal.", EnableDNSHostnames: &enabled,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := NewResource()
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)
	state := vpcValue(t, tfType, nil)

	update := func(plan, config tftypes.Value) *resource.UpdateResponse {
		resp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Update(ctx, resource.UpdateRequest{
			Plan:   tfsdk.Plan{Schema: schemaResp.Schema, Raw: plan},
			Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: config},
			State:  tfsdk.State{Schema: schemaResp.Schema, Raw: state},
		}, resp)
		return resp
	}

	changed := vpcValue(t, tfType, map[string]tftypes.Value{"dns_label": tftypes.NewValue(tftypes.String, "other")})
	resp := update(changed, changed)
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Summary(), "Cannot Change") {
		t.Fatalf("a dns_label change must be refused at apply, got %v", resp.Diagnostics)
	}
	if len(puts) != 0 {
		t.Fatalf("the refusal must come before any write, got %d PUT(s)", len(puts))
	}

	// Rename with enable_dns_hostnames unset in config: the plan carries the
	// state's value, but nothing is echoed for it.
	renamed := vpcValue(t, tfType, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "prod2")})
	renamedConfig := vpcValue(t, tfType, map[string]tftypes.Value{
		"name":                 tftypes.NewValue(tftypes.String, "prod2"),
		"enable_dns_hostnames": tftypes.NewValue(tftypes.Bool, nil),
		"dns_label":            tftypes.NewValue(tftypes.String, nil),
	})
	if resp := update(renamed, renamedConfig); resp.Diagnostics.HasError() {
		t.Fatalf("rename: %v", resp.Diagnostics)
	}
	if _, sent := puts[len(puts)-1]["enableDnsHostnames"]; sent {
		t.Errorf("an unconfigured enable_dns_hostnames was sent: %v", puts[len(puts)-1])
	}
	if _, sent := puts[len(puts)-1]["dnsLabel"]; sent {
		t.Errorf("an update sent dnsLabel: %v", puts[len(puts)-1])
	}

	// Configured false: sent, and the read-back lands in state.
	off := vpcValue(t, tfType, map[string]tftypes.Value{"enable_dns_hostnames": tftypes.NewValue(tftypes.Bool, false)})
	resp = update(off, off)
	if resp.Diagnostics.HasError() {
		t.Fatalf("toggle: %v", resp.Diagnostics)
	}
	if puts[len(puts)-1]["enableDnsHostnames"] != false {
		t.Errorf("enableDnsHostnames = %v, want false", puts[len(puts)-1]["enableDnsHostnames"])
	}
	var model VPCModel
	resp.State.Get(ctx, &model)
	if model.EnableDNSHostnames.ValueBool() || model.DNSDomain.ValueString() != "prod.ck4to7.omkcmwdd.fm.internal." {
		t.Errorf("state after toggle: %v / %v", model.EnableDNSHostnames, model.DNSDomain)
	}
}

// An explicit label already held in the tenant fails the create's operation
// with VPCDNSLabelTaken; the diagnostic points at dns_label and says nothing
// was created.
func TestCreate_DNSLabelTakenIsAnAttributeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/t-1/vpcs":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["dnsLabel"] != "prod" {
				t.Errorf("create sent dnsLabel %v, want prod", body["dnsLabel"])
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operationId": "op-1", "status": "pending"})
		case r.URL.Path == "/v1/tenants/t-1/operations/op-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-1", "status": "failed", "errorCode": "VPCDNSLabelTaken",
				"error": `dns label "prod" is already used by another VPC`,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := NewResource()
	r.(*vpcResource).pollInterval = 5 * time.Millisecond
	r.(*vpcResource).pollTimeout = 200 * time.Millisecond
	configureVPCResource(t, r, c)
	schemaResp := getVPCSchema(t)
	ctx := context.Background()
	tfType := schemaResp.Schema.Type().TerraformType(ctx)

	plan := vpcValue(t, tfType, map[string]tftypes.Value{
		"id":                   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"status":               tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"is_default":           tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
		"subnet_count":         tftypes.NewValue(tftypes.Number, tftypes.UnknownValue),
		"created_at":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"updated_at":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"dns_domain":           tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"enable_dns_hostnames": tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue),
	})
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: plan}}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the taken label to error")
	}
	d := resp.Diagnostics.Errors()[0]
	if d.Summary() != "VPC DNS Label Already Taken" || !strings.Contains(d.Detail(), "Nothing was created") {
		t.Errorf("got %q: %s", d.Summary(), d.Detail())
	}
	if !resp.State.Raw.IsNull() {
		t.Error("a refused create must record no state")
	}
}
