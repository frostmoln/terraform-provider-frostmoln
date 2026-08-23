package load_balancer

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// upgradeFrom runs the v0 -> v1 state upgrade against a prior state whose
// provider_type holds the given value (nil for an absent one) and returns the
// resulting `type`.
func upgradeFrom(t *testing.T, priorProviderType any) (string, bool) {
	t.Helper()
	ctx := context.Background()
	r := &loadBalancerResource{}

	upgraders := r.UpgradeState(ctx)
	up, ok := upgraders[0]
	if !ok {
		t.Fatal("no upgrader registered for schema version 0")
	}

	priorSchema := up.PriorSchema
	if priorSchema == nil {
		t.Fatal("upgrader has no PriorSchema")
	}
	priorType := priorSchema.Type().TerraformType(ctx)

	str := func(v any) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	priorValue := tftypes.NewValue(priorType, map[string]tftypes.Value{
		"id":                  str("lb-1"),
		"name":                str("test-lb"),
		"vpc_id":              str("vpc-1"),
		"subnet_id":           str("subnet-1"),
		"description":         str(nil),
		"vip_address":         str("10.0.0.5"),
		"scheme":              str("internal"),
		"public_ip_id":        str(nil),
		"public_ip_address":   str(nil),
		"provider_type":       str(priorProviderType),
		"flavor_id":           str(nil),
		"tags":                tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"vip_port_id":         str("port-1"),
		"status":              str("active"),
		"provisioning_status": str("ACTIVE"),
		"operating_status":    str("ONLINE"),
		"created_at":          str("2026-01-01T00:00:00Z"),
		"updated_at":          str(nil),
	})

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)

	req := resource.UpgradeStateRequest{
		State: &tfsdk.State{Raw: priorValue, Schema: *priorSchema},
	}
	resp := &resource.UpgradeStateResponse{
		State: tfsdk.State{Schema: currentSchema.Schema},
	}
	up.StateUpgrader(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("upgrade produced diagnostics: %v", resp.Diagnostics)
	}

	var out LoadBalancerModel
	if diags := resp.State.Get(ctx, &out); diags.HasError() {
		t.Fatalf("reading upgraded state: %v", diags)
	}
	return out.Type.ValueString(), out.Type.IsNull()
}

// TestUpgradeStateMapsProviderTypeToType is the guard for the single highest
// risk in the whole rename.
//
// provider_type forced replacement and carried a schema default, so it is
// present in every state a customer already holds. If the upgrade does not move
// its value onto `type`, the first plan after the provider bump proposes to
// DESTROY AND RECREATE every load balancer -- a new VIP and an outage, for a
// rename. These cases are the difference between a silent no-op upgrade and
// that.
func TestUpgradeStateMapsProviderTypeToType(t *testing.T) {
	tests := []struct {
		name  string
		prior any
		want  string
	}{
		{name: "legacy amphora becomes l7", prior: "amphora", want: "l7"},
		{name: "legacy ovn becomes l4", prior: "ovn", want: "l4"},
		{name: "absent resolves to the old default, not null", prior: nil, want: "l7"},
		{name: "empty resolves to the old default", prior: "", want: "l7"},
		{name: "already-canonical value is preserved", prior: "l4", want: "l4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, isNull := upgradeFrom(t, tt.prior)
			if isNull {
				t.Fatal("upgraded type is null; the next plan would propose a replacement")
			}
			if got != tt.want {
				t.Errorf("upgraded type = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUpgradeStatePreservesEveryOtherAttribute: an attribute dropped by the
// upgrade is silently erased from state, and for a ForceNew attribute that also
// means a replacement on the next plan. Deriving the prior schema from the
// current one is what prevents that, so assert the outcome rather than trusting
// the derivation.
func TestUpgradeStatePreservesEveryOtherAttribute(t *testing.T) {
	ctx := context.Background()
	r := &loadBalancerResource{}
	up := r.UpgradeState(ctx)[0]

	var currentSchema resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &currentSchema)

	for name := range currentSchema.Schema.Attributes {
		if name == "type" {
			continue // renamed; covered above
		}
		if _, ok := up.PriorSchema.Attributes[name]; !ok {
			t.Errorf("attribute %q is missing from the v0 prior schema; its value would be dropped on upgrade", name)
		}
	}
	if _, ok := up.PriorSchema.Attributes["provider_type"]; !ok {
		t.Error("v0 prior schema must declare provider_type")
	}
	if _, ok := up.PriorSchema.Attributes["type"]; ok {
		t.Error("v0 prior schema must not declare type")
	}
	if up.PriorSchema.Version != 0 {
		t.Errorf("prior schema version = %d, want 0", up.PriorSchema.Version)
	}
}

// TestPriorSchemaDecodesLiteralV0JSON feeds a LITERAL v0 state blob — the shape
// a real .tfstate holds — through the prior schema, rather than through a value
// built from that same schema.
//
// The other tests in this file construct their input from priorSchema.Type(),
// which makes them tautological about types: they would keep passing if a
// future change altered some other attribute's type, while real v0 JSON on disk
// stopped decoding and every customer still on version 0 hit a hard "Unable to
// Read Previously Saved State" error on their next plan. This is the test that
// actually pins the on-disk contract.
func TestPriorSchemaDecodesLiteralV0JSON(t *testing.T) {
	ctx := context.Background()
	r := &loadBalancerResource{}
	prior := r.UpgradeState(ctx)[0].PriorSchema
	if prior == nil {
		t.Fatal("upgrader has no PriorSchema")
	}

	const v0 = `{
	  "id": "lb-1",
	  "name": "web",
	  "vpc_id": "vpc-1",
	  "subnet_id": "subnet-1",
	  "description": null,
	  "vip_address": "10.0.0.5",
	  "scheme": "internal",
	  "public_ip_id": null,
	  "public_ip_address": null,
	  "provider_type": "amphora",
	  "flavor_id": null,
	  "tags": {"env": "prod"},
	  "vip_port_id": "port-1",
	  "status": "active",
	  "provisioning_status": "ACTIVE",
	  "operating_status": "ONLINE",
	  "created_at": "2026-01-01T00:00:00Z",
	  "updated_at": null
	}`

	raw := tfprotov6.RawState{JSON: []byte(v0)}
	val, err := raw.UnmarshalWithOpts(
		prior.Type().TerraformType(ctx),
		tfprotov6.UnmarshalOpts{ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true}},
	)
	if err != nil {
		t.Fatalf("a real v0 state no longer decodes against the prior schema: %v", err)
	}
	if val.IsNull() {
		t.Fatal("decoded v0 state is null")
	}
}
