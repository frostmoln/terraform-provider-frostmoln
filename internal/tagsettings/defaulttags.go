package tagsettings

import (
	"context"
	"fmt"
	"net/url"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// Tenant default tags (frostmoln_tenant_default_tags): the tags the platform
// copies onto every taggable resource created in a tenant.
//
//	GET  /v1/tenants/{tid}/default-tags   -> {"tags": {...}}  ({} when none)
//	PUT  /v1/tenants/{tid}/default-tags   {"tags": {...}}     full replace; {} clears
//
// A default is written onto every kind of resource, so identity holds it to the
// intersection of their tag rules (ValidateTenantDefaultTags) — the same rules
// the provider's own default_tags are held to, which internal/tftags already
// mirrors. The plan-time check below reuses tftags.CheckDefaultTag rather than
// keeping a second copy of the rules.

// DefaultTags is the body of both routes. Tags is never nil on a request: the
// server refuses a body without `tags` (so a typo cannot read as "clear"), and
// an explicit {} is how the set is cleared.
type DefaultTags struct {
	Tags map[string]string `json:"tags"`
}

// DefaultTagsPath is a tenant's default-tag set. The tenant id is escaped so
// it stays one path segment; the schema validator refuses a non-UUID before it
// gets here.
func DefaultTagsPath(tenantID string) string {
	return fmt.Sprintf("/v1/tenants/%s/default-tags", url.PathEscape(tenantID))
}

// GetDefaultTags reads a tenant's default tags, never nil.
func GetDefaultTags(ctx context.Context, c *client.Client, tenantID string) (map[string]string, error) {
	resp, err := c.Get(ctx, DefaultTagsPath(tenantID), nil)
	if err != nil {
		return nil, err
	}
	body, err := client.ParseResponse[DefaultTags](resp)
	if err != nil {
		return nil, err
	}
	if body.Tags == nil {
		return map[string]string{}, nil
	}
	return body.Tags, nil
}

// PutDefaultTags replaces a tenant's default tags and returns the stored set.
func PutDefaultTags(ctx context.Context, c *client.Client, tenantID string, tags map[string]string) (map[string]string, error) {
	if tags == nil {
		tags = map[string]string{}
	}
	resp, err := c.Put(ctx, DefaultTagsPath(tenantID), DefaultTags{Tags: tags})
	if err != nil {
		return nil, err
	}
	body, err := client.ParseResponse[DefaultTags](resp)
	if err != nil {
		return nil, err
	}
	if body.Tags == nil {
		return map[string]string{}, nil
	}
	return body.Tags, nil
}

// CheckTenantID returns why id cannot be a tenant id, or "": uuid.Parse, as
// identity parses the path segment, in the canonical spelling (the same
// convergence rule as CheckOrganizationID).
func CheckTenantID(id string) string {
	canonical, err := CanonicalUUID(id)
	if err != nil {
		return fmt.Sprintf("%q is not a tenant id (a UUID).", clip(id))
	}
	if canonical != id {
		return fmt.Sprintf("Write the tenant id as %q, the form the platform reports.", canonical)
	}
	return ""
}

// TenantIDValidator validates a tenant id (CheckTenantID).
func TenantIDValidator() validator.String {
	return checkValidator{"Invalid Tenant ID", "a tenant id (UUID)", CheckTenantID}
}

// DefaultTagsValidator checks a tenant default-tag set against identity's
// ValidateTenantDefaultTags, in its order: the count, then each key in sorted
// order (reserved namespace, key shape, value). An unknown value has its key
// checked and its value left to the apply; a null value is refused, as the
// server refuses one.
func DefaultTagsValidator() validator.Map {
	return defaultTagsValidator{}
}

type defaultTagsValidator struct{}

func (defaultTagsValidator) Description(_ context.Context) string {
	return fmt.Sprintf("at most %d tags, each acceptable on every kind of resource", tftags.MaxDefaultTags)
}

func (v defaultTagsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (defaultTagsValidator) ValidateMap(_ context.Context, req validator.MapRequest, resp *validator.MapResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	elems := req.ConfigValue.Elements()
	if len(elems) > tftags.MaxDefaultTags {
		resp.Diagnostics.AddAttributeError(req.Path, "Too Many Default Tags",
			fmt.Sprintf("%d default tags (maximum %d). Default tags are added to every taggable resource created "+
				"in the tenant, so they are kept few enough to leave room for each resource's own tags.",
				len(elems), tftags.MaxDefaultTags))
	}
	keys := make([]string, 0, len(elems))
	for k := range elems {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sv, _ := elems[k].(types.String)
		if sv.IsNull() {
			resp.Diagnostics.AddAttributeError(req.Path.AtMapKey(k), "Invalid Default Tag Value",
				fmt.Sprintf("The value of %q is null: set a string (it may be empty), or remove the key.", k))
			continue
		}
		var value *string
		if !sv.IsUnknown() {
			s := sv.ValueString()
			value = &s
		}
		if problem := tftags.CheckDefaultTag(k, value); problem != nil {
			resp.Diagnostics.AddAttributeError(req.Path.AtMapKey(k), problem.Summary, problem.Detail)
		}
	}
}
