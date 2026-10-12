package tftags

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ErrOfferTagsUnavailable is what an update's current-tags read returns when the
// offer answered the object but could not read its tag store
// (`tagsUnavailable: true`): the write that would follow is refused by the same
// outage (503 tags_unavailable), and computing it against an unknown set could
// drop a key Terraform does not manage.
var ErrOfferTagsUnavailable = errors.New("the platform could not read this resource's tags right now " +
	"(its tag store is temporarily unavailable); the resource itself is unaffected — apply again shortly")

// KeepOfferTagsWhenUnavailable is the read-back for an offer response that
// carries the object but not its tags — `tagsUnavailable: true` with `tags:
// null` (the container registry, whose objects live in Harbor and whose tags
// live in the registry's own database). The object is real and current; only
// its tags are unknown, so Terraform keeps the tags it already holds rather
// than recording null or {} — either would plan a spurious change, and the next
// apply would then write it — and says so with a warning.
//
// tags and tagsAll are the prior values (state on a refresh, plan on a
// create/update). A tags_all that is not known yet (a create's or an update's
// plan) becomes fallback — the set the write sent — or an empty map; a null one
// (a resource just imported) stays null; tags is kept as configured, null
// staying null.
func KeepOfferTagsWhenUnavailable(ctx context.Context, subject string, tags, tagsAll types.Map, fallback map[string]string, diags *diag.Diagnostics) (types.Map, types.Map) {
	diags.AddAttributeWarning(path.Root("tags_all"), "Tags Temporarily Unavailable",
		"The platform answered "+subject+" but could not read its resource tags right now. Terraform kept the "+
			"tags it already knew instead of recording them as empty; nothing about the resource itself is "+
			"affected. Refresh again shortly to read the current tags.")
	if tags.IsUnknown() {
		tags = types.MapNull(types.StringType)
	}
	if tagsAll.IsUnknown() {
		if fallback == nil {
			fallback = map[string]string{}
		}
		all, d := types.MapValueFrom(ctx, types.StringType, fallback)
		diags.Append(d...)
		tagsAll = all
	}
	return tags, tagsAll
}
