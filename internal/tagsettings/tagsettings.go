// Package tagsettings is the provider's side of the platform's customer tag
// settings: organization tag COLOUR rules (frostmoln_tag_color,
// frostmoln_tag_colors) and tenant DEFAULT TAGS (frostmoln_tenant_default_tags,
// defaulttags.go) — the wire types, the request paths, the plan-time checks,
// and how a tenant-scoped provider finds the organization that owns its tenant.
//
// The server contract is identity's (api/openapi/identity-api.yaml,
// internal/domain/tag_settings.go, internal/handler/http/tag_settings.go):
//
//	GET/POST          /v1/organizations/{org}/tag-colors
//	GET/PUT/DELETE    /v1/organizations/{org}/tag-colors/{ruleId}
//	GET               /v1/tenants/{tid}/tag-colors   -> {organizationId, rules}
//	GET/PUT           /v1/tenants/{tid}/default-tags -> {tags}
package tagsettings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// Error codes the colour-rule routes answer that a caller branches on.
const (
	// CodeRuleExists is the 409 for a create (or a PUT retarget) whose key and
	// value another rule of the organization already holds.
	// error.details.ruleId names that rule when the server knows it.
	CodeRuleExists = "TAG_COLOR_RULE_EXISTS"
	// CodeRuleLimitReached is the 409 for a genuinely new rule over the
	// organization's cap (error.details.max).
	CodeRuleLimitReached = "TAG_COLOR_RULE_LIMIT_REACHED"
)

// Rule is a tag colour rule as the API returns it.
//
// Value is a POINTER because null and "" are different rules: null matches any
// value of Key, "" matches exactly the empty value. The server always sends the
// field (null, never omitted), and encoding/json decodes a JSON null into a nil
// pointer and "" into a pointer to "", so the distinction survives the decode.
type Rule struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Value     *string `json:"value"`
	Color     string  `json:"color"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

// RuleRequest is the body of a create (POST) and a full replace (PUT).
//
// Value is always sent: nil marshals as `"value": null` (any value) and a
// pointer to "" as `"value": ""` (only the empty value). It carries no
// omitempty. Today's server reads an omitted value as "any value" on both POST
// and PUT, so omitting a nil would mean the same thing — but the PUT is a full
// replace, and an explicit null is what states the whole rule rather than
// relying on what absence means. Keep it a pointer: a plain string with
// omitempty would drop "" and turn the empty-value rule into the any-value one.
type RuleRequest struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
	Color string  `json:"color"`
}

type ruleList struct {
	Rules []Rule `json:"rules"`
}

type tenantRuleList struct {
	OrganizationID string `json:"organizationId"`
	Rules          []Rule `json:"rules"`
}

// OrgRulesPath is the colour-rule collection of an organization. The id is
// percent-escaped so it stays one literal path segment; dot segments are
// refused before this is reached (the schema validator and ParseImportID),
// because path.Join in the client would collapse them.
func OrgRulesPath(orgID string) string {
	return fmt.Sprintf("/v1/organizations/%s/tag-colors", url.PathEscape(orgID))
}

// OrgRulePath is one colour rule of an organization.
func OrgRulePath(orgID, ruleID string) string {
	return OrgRulesPath(orgID) + "/" + url.PathEscape(ruleID)
}

// TenantRulesPath is the provider tenant's read-only view of the colour rules
// of the organization that owns it.
func TenantRulesPath(c *client.Client) string {
	return c.TenantPath("/tag-colors")
}

// ErrOrganizationUnresolved is returned when the platform answered the tenant
// route but did not name the organization that owns the tenant — a platform
// that predates the `organizationId` field. The provider then refuses rather
// than guessing: the credential's home organization is NOT necessarily the one
// that owns the provider's tenant (an org-invited user's is not), and a rule
// written to the wrong organization colours the wrong tenants.
var ErrOrganizationUnresolved = errors.New("the platform did not report which organization owns the tenant")

// ListOrgRules reads an organization's colour rules.
func ListOrgRules(ctx context.Context, c *client.Client, orgID string) ([]Rule, error) {
	resp, err := c.Get(ctx, OrgRulesPath(orgID), nil)
	if err != nil {
		return nil, err
	}
	list, err := client.ParseResponse[ruleList](resp)
	if err != nil {
		return nil, err
	}
	return list.Rules, nil
}

// ListTenantRules reads the colour rules that apply in the provider's tenant,
// with the id of the organization that owns it. orgID is "" when the platform
// does not report it (see ErrOrganizationUnresolved); the rules are still that
// organization's.
func ListTenantRules(ctx context.Context, c *client.Client) (orgID string, rules []Rule, err error) {
	resp, err := c.Get(ctx, TenantRulesPath(c), nil)
	if err != nil {
		return "", nil, err
	}
	list, err := client.ParseResponse[tenantRuleList](resp)
	if err != nil {
		return "", nil, err
	}
	return list.OrganizationID, list.Rules, nil
}

// resolvedOrgs caches ResolveOrganizationID per client (one per provider
// configuration, bound to one tenant). The organization that owns a tenant does
// not change under a running apply, and without the cache every rule with
// organization_id omitted would re-read the tenant's whole rule set on every
// plan. Only successes are cached.
var resolvedOrgs sync.Map // *client.Client -> string

// ResolveOrganizationID returns the id of the organization that owns the
// provider's tenant, as the platform reports it on the tenant route, in
// canonical form. It never falls back to anything else.
func ResolveOrganizationID(ctx context.Context, c *client.Client) (string, error) {
	if cached, ok := resolvedOrgs.Load(c); ok {
		return cached.(string), nil
	}
	orgID, _, err := ListTenantRules(ctx, c)
	if err != nil {
		return "", err
	}
	if orgID == "" {
		return "", ErrOrganizationUnresolved
	}
	canonical, err := CanonicalUUID(orgID)
	if err != nil {
		return "", fmt.Errorf("the platform reported organization id %q, which is not a UUID: %w", orgID, err)
	}
	resolvedOrgs.Store(c, canonical)
	return canonical, nil
}

// IsForbidden reports whether err is a 403 — for the tag-settings routes, a
// credential without the organizations:* scope, or a caller who is not an
// active member of the organization.
func IsForbidden(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden
}

// UnresolvedOrganizationDetail is the diagnostic detail for
// ErrOrganizationUnresolved, naming the tenant and the fix.
func UnresolvedOrganizationDetail(tenantID string) string {
	return fmt.Sprintf("organization_id is not set, so the provider asked the platform which organization owns "+
		"tenant %q, and the answer did not name one. Set organization_id to the id of the organization whose tag "+
		"colours this should manage. The provider does not fall back to your account's home organization: it is "+
		"not necessarily the one that owns this tenant, and colour rules apply to every tenant of the organization "+
		"they are written to.", tenantID)
}

// RuleExistsID returns the id of the existing rule a TAG_COLOR_RULE_EXISTS
// refusal names, and whether err is that refusal at all. The id is "" when the
// server could not name it (its unique-index backstop path).
func RuleExistsID(err error) (string, bool) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeRuleExists {
		return "", false
	}
	id, _ := apiErr.Details["ruleId"].(string)
	return id, true
}
