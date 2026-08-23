package load_balancer

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// loadBalancerModelV0 is the schema-version-0 state model. It differs from the
// current model in exactly one attribute: the type was called `provider_type`
// and held the implementation driver name ("amphora"/"ovn").
type loadBalancerModelV0 struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	VPCID              types.String `tfsdk:"vpc_id"`
	SubnetID           types.String `tfsdk:"subnet_id"`
	Description        types.String `tfsdk:"description"`
	VIPAddress         types.String `tfsdk:"vip_address"`
	Scheme             types.String `tfsdk:"scheme"`
	PublicIPID         types.String `tfsdk:"public_ip_id"`
	PublicIPAddress    types.String `tfsdk:"public_ip_address"`
	Provider           types.String `tfsdk:"provider_type"`
	FlavorID           types.String `tfsdk:"flavor_id"`
	Tags               types.Map    `tfsdk:"tags"`
	VIPPortID          types.String `tfsdk:"vip_port_id"`
	Status             types.String `tfsdk:"status"`
	ProvisioningStatus types.String `tfsdk:"provisioning_status"`
	OperatingStatus    types.String `tfsdk:"operating_status"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
}

// priorSchemaV0 derives the version-0 schema from the current one by renaming
// the single attribute that changed.
//
// What the framework actually uses this for is narrow: only PriorSchema.Type()
// is read, to decode the stored JSON. Defaults, validators and plan modifiers
// are never consulted at upgrade time, so reusing the current attribute
// definitions is inert.
//
// Deriving rather than restating all eighteen attributes keeps the two from
// drifting on the axis that matters HERE -- an attribute present on disk in v0
// state but missing from this schema would be dropped from the upgraded state.
//
// The derivation is not free of risk, and the risk is the opposite of what it
// looks like: because it tracks the CURRENT types, changing another attribute's
// type later (say tags from map(string)) would make this schema claim the new
// type while real v0 JSON on disk still holds the old one, and the decode would
// fail for everyone still on v0. The test below cannot catch that -- it builds
// its input from this same schema, so it is tautological on types. A literal v0
// JSON fixture is what would catch it; noted here rather than left implicit.
func (r *loadBalancerResource) priorSchemaV0(ctx context.Context) *schema.Schema {
	var current resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &current)

	attrs := make(map[string]schema.Attribute, len(current.Schema.Attributes))
	for name, attr := range current.Schema.Attributes {
		attrs[name] = attr
	}
	attrs["provider_type"] = attrs["type"]
	delete(attrs, "type")

	prior := current.Schema
	prior.Attributes = attrs
	prior.Version = 0
	return &prior
}

// UpgradeState migrates state written by a provider version that called the
// attribute `provider_type` and stored the implementation driver name.
//
// WHY THIS IS REQUIRED, not a nicety. `provider_type` forces replacement, and
// it carried a schema default, so it is present in EVERY existing state
// whether or not the practitioner ever set it. Without this upgrader the first
// plan after the provider bump sees the attribute vanish and `type` appear with
// its own default, and proposes to destroy and recreate every load balancer the
// customer has -- which means a new VIP and an outage, for a rename.
//
// The value is canonicalised at the same time: "amphora" becomes "l7" and "ovn"
// becomes "l4", so state matches what a current configuration says and what the
// API now returns.
func (r *loadBalancerResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {
			PriorSchema: r.priorSchemaV0(ctx),
			StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
				var old loadBalancerModelV0
				resp.Diagnostics.Append(req.State.Get(ctx, &old)...)
				if resp.Diagnostics.HasError() {
					return
				}

				// An absent prior value resolves to l7, not null. The v0 schema
				// defaulted to "amphora", so an existing load balancer is an l7
				// one even when the practitioner never wrote the attribute.
				// Carrying a null through instead would leave state disagreeing
				// with the new default and plan a replacement -- the exact
				// outcome this upgrader exists to prevent.
				newType := "l7"
				if !old.Provider.IsNull() && !old.Provider.IsUnknown() && old.Provider.ValueString() != "" {
					newType = canonicalLBType(old.Provider.ValueString())
				}

				resp.Diagnostics.Append(resp.State.Set(ctx, LoadBalancerModel{
					ID:                 old.ID,
					Name:               old.Name,
					VPCID:              old.VPCID,
					SubnetID:           old.SubnetID,
					Description:        old.Description,
					VIPAddress:         old.VIPAddress,
					Scheme:             old.Scheme,
					PublicIPID:         old.PublicIPID,
					PublicIPAddress:    old.PublicIPAddress,
					Type:               types.StringValue(newType),
					FlavorID:           old.FlavorID,
					Tags:               old.Tags,
					VIPPortID:          old.VIPPortID,
					Status:             old.Status,
					ProvisioningStatus: old.ProvisioningStatus,
					OperatingStatus:    old.OperatingStatus,
					CreatedAt:          old.CreatedAt,
					UpdatedAt:          old.UpdatedAt,
				})...)
			},
		},
	}
}
