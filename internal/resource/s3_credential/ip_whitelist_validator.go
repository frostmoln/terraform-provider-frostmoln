package s3_credential

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// ipWhitelistUnavailableDetail is the plan-time refusal for a non-empty
// ip_whitelist.
//
// The platform refuses a non-empty ipWhitelist on create with 400
// ip_whitelist_unavailable (security audit H-12): the object-storage backend
// does not yet see the real client address, so the restriction could not be
// enforced as written. Every attribute of this resource is RequiresReplace, so
// without this validator a config carrying ip_whitelist would plan
// destroy-then-create, the destroy would succeed and the create would fail —
// the old key gone and no replacement. Refusing at plan time stops that before
// anything is destroyed.
const ipWhitelistUnavailableDetail = "IP allow-lists for S3 credentials are temporarily unavailable, and the " +
	"platform refuses a credential that sets one. Remove ip_whitelist and restrict " +
	"access with allowed_buckets and allowed_actions instead.\n\n" +
	"This is checked at plan time on purpose: changing ip_whitelist replaces the credential, so the " +
	"existing key would be deleted before the refused create — leaving you with no credential at all."

// ipWhitelistMustBeEmpty rejects a known, non-empty ip_whitelist. Null, unknown
// and [] pass: an unknown value cannot be judged at plan time, and the server
// still refuses it at apply.
type ipWhitelistMustBeEmpty struct{}

func (ipWhitelistMustBeEmpty) Description(context.Context) string {
	return "must be empty or unset: IP allow-lists are temporarily unavailable"
}

func (v ipWhitelistMustBeEmpty) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (ipWhitelistMustBeEmpty) ValidateList(_ context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() || len(req.ConfigValue.Elements()) == 0 {
		return
	}
	resp.Diagnostics.AddAttributeError(req.Path, "IP allow-lists are temporarily unavailable", ipWhitelistUnavailableDetail)
}
