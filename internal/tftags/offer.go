package tftags

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The managed-offer tag policy (MANAGED-OFFER-TAGS-PLAN §2, servicekit
// resourcetags.Standard — the DNS-zone policy): what every offer service enforces on
// a resource's MERGED tag set. Checked here at plan time so a bad configuration fails
// before anything is created; the service stays the authority (it also merges the
// tenant's default tags, which this provider cannot see).
const (
	// OfferMaxTags is the per-resource key budget, defaults included.
	OfferMaxTags = 32
	// OfferMaxKeyBytes / OfferMaxValueBytes bound a key and a value in BYTES of UTF-8.
	OfferMaxKeyBytes   = 64
	OfferMaxValueBytes = 256
)

var offerKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._:/-]*[a-zA-Z0-9])?$`)

// IsOfferReservedKey reports whether key is in the platform namespace an offer
// reserves: frostmoln_ or frostmoln-, in any letter case.
func IsOfferReservedKey(key string) bool {
	k := strings.ToLower(key)
	return strings.HasPrefix(k, "frostmoln_") || strings.HasPrefix(k, "frostmoln-")
}

// OfferTagProblem returns why one tag breaks the offer policy, or "". value is nil
// when it is not known yet.
func OfferTagProblem(key string, value *string) string {
	switch {
	case IsOfferReservedKey(key):
		return fmt.Sprintf("tag key %q is reserved: keys beginning frostmoln_ or frostmoln- (any letter case) belong to the platform", key)
	case key == "" || len(key) > OfferMaxKeyBytes:
		return fmt.Sprintf("tag key %q must be 1 to %d bytes", key, OfferMaxKeyBytes)
	case !offerKeyRegex.MatchString(key):
		return fmt.Sprintf("tag key %q may contain only letters, digits and . _ : / - and must start and end with a letter or digit", key)
	case value == nil:
		return ""
	case len(*value) > OfferMaxValueBytes:
		return fmt.Sprintf("the value of tag %q is %d bytes; at most %d bytes are allowed", key, len(*value), OfferMaxValueBytes)
	}
	for _, r := range *value {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return fmt.Sprintf("the value of tag %q contains a control or bidirectional-override character", key)
		}
	}
	return ""
}

// OfferTagsValidator is the `tags` validator of every managed-offer resource.
func OfferTagsValidator() validator.Map { return offerTagsValidator{} }

type offerTagsValidator struct{}

func (offerTagsValidator) Description(context.Context) string {
	return fmt.Sprintf("at most %d tags; keys 1-%d bytes matching %s, not beginning frostmoln_ or frostmoln- (any case); values at most %d bytes with no control or bidirectional-override characters",
		OfferMaxTags, OfferMaxKeyBytes, offerKeyRegex.String(), OfferMaxValueBytes)
}

func (v offerTagsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (offerTagsValidator) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > OfferMaxTags {
		resp.Diagnostics.AddAttributeError(req.Path, "Too Many Tags",
			fmt.Sprintf("%d tags are set; a managed-offer resource takes at most %d, the provider's default_tags and the tenant's default tags included.", len(elems), OfferMaxTags))
	}
	keys := make([]string, 0, len(elems))
	for k := range elems {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var value *string
		if s, ok := elems[k].(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			v := s.ValueString()
			value = &v
		}
		if problem := OfferTagProblem(k, value); problem != "" {
			resp.Diagnostics.AddAttributeError(req.Path.AtMapKey(k), "Invalid Tag", problem+".")
		}
	}
}

// WarnOfferTagBudget is the plan-time half an offer resource's ModifyPlan adds: the
// provider's default_tags merged with the resource's tags must fit the offer's
// 32-key budget. A warning, not an error — a plan error would also abort a destroy —
// and the service refuses the write anyway; this says why before the apply.
// (default_tags can never carry an offer-reserved key: CheckDefaultTag refuses the
// frostmoln_ and frostmoln- prefixes in any case when the provider is configured.)
func WarnOfferTagBudget(ctx context.Context, d Defaults, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || d.Unknown {
		return
	}
	var tags types.Map
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("tags"), &tags)...)
	own, ok := knownMap(ctx, tags, &resp.Diagnostics)
	if !ok {
		return
	}
	merged := map[string]bool{}
	for k := range d.Tags {
		merged[k] = true
	}
	for k := range own {
		merged[k] = true
	}
	if len(merged) > OfferMaxTags {
		resp.Diagnostics.AddAttributeWarning(path.Root("tags"), "Too Many Tags With default_tags",
			fmt.Sprintf("The provider's default_tags and this resource's tags make %d keys together; the platform "+
				"accepts at most %d (the tenant's default tags count too), so the apply will be refused.",
				len(merged), OfferMaxTags))
	}
}

// OfferTagsProblems returns why a tag map a write is about to send breaks the offer
// policy, one line per key, sorted; empty when it is fine. The plan-time validator
// skips an unknown map, so a write re-checks the map it resolved.
func OfferTagsProblems(tags map[string]string) []string {
	var out []string
	for k, v := range tags {
		v := v
		if p := OfferTagProblem(k, &v); p != "" {
			out = append(out, k+": "+p)
		}
	}
	if len(tags) > OfferMaxTags {
		out = append(out, fmt.Sprintf("%d keys; the platform accepts at most %d", len(tags), OfferMaxTags))
	}
	sort.Strings(out)
	return out
}

// TooManyOfferTags is the budget check over known key sets: the number of distinct
// keys across them when it exceeds OfferMaxTags, else 0.
func TooManyOfferTags(sets ...map[string]string) int {
	keys := map[string]bool{}
	for _, s := range sets {
		for k := range s {
			keys[k] = true
		}
	}
	if len(keys) > OfferMaxTags {
		return len(keys)
	}
	return 0
}
