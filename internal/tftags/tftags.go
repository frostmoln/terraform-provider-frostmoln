// Package tftags renders a Terraform tag map into the shape the Frostmoln API
// expects on an in-place update.
//
// It exists because "the practitioner removed `tags` from the config" and "the
// practitioner has no opinion about tags" are different requests that a naive
// conversion collapses into the same one. Every Frostmoln update endpoint merges
// tags on `if req.Tags != nil`, so an omitted field means KEEP — which makes an
// omitted-on-null encoding unable to express "clear them" at all.
package tftags

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ForUpdate renders the tag map the backend should end up with.
//
// A NULL attribute — `tags` removed from the config — becomes an EMPTY MAP, so
// the backend clears them. The corresponding struct field must therefore carry
// no `omitempty`: that option drops an empty non-nil map, and the two together
// made clearing impossible, leaving the backend holding tags the state said were
// gone. The fm CLI already normalises nil to an empty map against these same
// endpoints, so a provider that omitted the field disagreed with it on one wire
// call — the cross-client divergence the platform rule exists to prevent.
//
// An UNKNOWN attribute stays nil: the value is not computable yet, so the honest
// request is "no opinion", which serialises to null and leaves the tags alone.
func ForUpdate(ctx context.Context, tags types.Map, diags *diag.Diagnostics) map[string]string {
	if tags.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	if !tags.IsNull() {
		diags.Append(tags.ElementsAs(ctx, &out, false)...)
	}
	return out
}
