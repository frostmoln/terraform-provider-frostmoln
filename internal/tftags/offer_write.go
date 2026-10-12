package tftags

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
)

// OfferTagsPolicyNote states the managed-offer tag policy in a `tags` description.
const OfferTagsPolicyNote = " At most 32 keys including the tenant's default tags, which the platform merges in " +
	"at create; a key is 1-64 bytes matching `^[a-zA-Z0-9]([a-zA-Z0-9._:/-]*[a-zA-Z0-9])?$`, a value at most " +
	"256 bytes; keys beginning `frostmoln_` or `frostmoln-` (any case) are reserved."

// OfferTagsAttribute is the `tags` attribute of a managed-offer resource
// (MANAGED-OFFER-TAGS-PLAN §2): Optional, the offer validator, and lead — what the
// tags are on and what changing them does — followed by the policy and TagsNote.
func OfferTagsAttribute(lead string) schema.MapAttribute {
	return schema.MapAttribute{
		Description: lead + OfferTagsPolicyNote + TagsNote,
		Optional:    true,
		ElementType: types.StringType,
		Validators:  []validator.Map{OfferTagsValidator()},
		PlanModifiers: []planmodifier.Map{
			mapplanmodifier.UseStateForUnknown(),
		},
	}
}

// OfferCustomerTags is the tag set a managed offer holds on a resource, minus the
// namespace the offer reserves (reservedmeta.FilterOffer). Every read-back, every
// update's pre-write read and every data source use it, so `tags` means the same
// thing on every surface.
func OfferCustomerTags(api map[string]string) map[string]string {
	return reservedmeta.FilterOffer(api)
}

// OfferTagsValue is a data source's `tags`: every customer tag the offer holds
// (OfferCustomerTags), an empty map when there are none.
func OfferTagsValue(api map[string]string) types.Map {
	elems := map[string]attr.Value{}
	for k, v := range OfferCustomerTags(api) {
		elems[k] = types.StringValue(v)
	}
	return types.MapValueMust(types.StringType, elems)
}

// OfferTagsToWrite returns the tag map a managed-offer update must send, or nil
// when the platform's set needs no change. Judged first against state (the plan
// was made from it); only when that says a write is due does it call current —
// the tags the resource holds right now, through OfferCustomerTags — and
// recompute against them, so a key added outside Terraform since the last refresh
// is kept, and an update that is not about tags costs no extra read. An error is
// current's: report it with CurrentTagsReadFailed.
func OfferTagsToWrite(ctx context.Context, d Defaults, planTags, stateTags, stateTagsAll types.Map, private PrivateReader,
	current func(context.Context) (map[string]string, error), diags *diag.Diagnostics) (*map[string]string, error) {
	prior := PriorOf(ctx, stateTags, stateTagsAll, private, diags)
	if _, changed := ForUpdate(ctx, d, planTags, prior, diags); !changed || diags.HasError() {
		return nil, nil
	}
	cur, err := current(ctx)
	if err != nil {
		return nil, err
	}
	tags, changed := ForUpdate(ctx, d, planTags, prior.WithCurrent(cur), diags)
	if !changed || diags.HasError() {
		return nil, nil
	}
	return &tags, nil
}
