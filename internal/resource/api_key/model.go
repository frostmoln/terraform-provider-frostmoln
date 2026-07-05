// Package api_key implements the fm_api_key Terraform resource.
package api_key

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// expiresAtSemanticEquality is a plan modifier that suppresses a diff when the
// configured expires_at and the prior state value denote the SAME instant — e.g.
// config "2027-01-01" against an imported/canonical state "2027-01-01T23:59:59Z".
// Without it, the bare-date form the docs recommend would force-replace an
// imported key (RequiresReplace), rotating its secret, on a plan that otherwise
// looks like a no-op. It must run BEFORE RequiresReplace so an equal instant
// yields no replacement; a genuinely different instant falls through and still
// forces replacement.
type expiresAtSemanticEquality struct{}

func (expiresAtSemanticEquality) Description(context.Context) string {
	return "Suppresses a diff when the new expires_at denotes the same instant as the prior value."
}

func (m expiresAtSemanticEquality) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (expiresAtSemanticEquality) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Only act with a concrete prior state and a known config value.
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if sameExpiryInstant(req.ConfigValue.ValueString(), req.StateValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}

// normalizeAPIKeyExpiry converts a user-supplied expires_at value to the
// complete RFC3339 timestamp identity requires. identity's ExpiresAt is a
// *time.Time that JSON-decodes only a quoted RFC3339 string, so a bare date is
// rejected as 400 "invalid request body". An empty value stays empty so the
// server applies its default (~1y). A YYYY-MM-DD date becomes the end of that
// day (23:59:59) in UTC (2027-01-01T23:59:59Z), so the key is valid through the
// named date; UTC — not the operator's local zone the fm CLI uses — keeps a
// Terraform plan deterministic across machines and CI runners. A full RFC3339
// value is canonicalized to UTC. Anything else is a friendly client-side error.
func normalizeAPIKeyExpiry(expires string) (string, error) {
	if expires == "" {
		return "", nil
	}
	if t, err := time.ParseInLocation("2006-01-02", expires, time.UTC); err == nil {
		eod := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
		return eod.Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, expires); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	return "", fmt.Errorf("expires_at must be a date (YYYY-MM-DD) or RFC3339 timestamp, got %q", expires)
}

// sameExpiryInstant reports whether two expires_at strings denote the same
// instant, by comparing their canonical (normalized) forms. It lets fromAPI
// preserve the user's config string (e.g. a bare date or a non-UTC offset) when
// it matches what the API returned, so state == config and Terraform sees no
// spurious diff after apply. A value that fails to normalize is never a match.
func sameExpiryInstant(a, b string) bool {
	na, err := normalizeAPIKeyExpiry(a)
	if err != nil || na == "" {
		return false
	}
	nb, err := normalizeAPIKeyExpiry(b)
	if err != nil || nb == "" {
		return false
	}
	return na == nb
}

// APIKeyModel is the Terraform state model for an API key.
type APIKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Scopes      types.List   `tfsdk:"scopes"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
	RateLimit   types.Int64  `tfsdk:"rate_limit"`
	Key         types.String `tfsdk:"key"`
	KeyPrefix   types.String `tfsdk:"key_prefix"`
	Status      types.String `tfsdk:"status"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiAPIKey is the API representation of an API key.
type apiAPIKey struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Key         string   `json:"key,omitempty"`
	KeyPrefix   string   `json:"keyPrefix"`
	Scopes      []string `json:"scopes,omitempty"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
	RateLimit   int      `json:"rateLimit,omitempty"`
	Status      string   `json:"status"`
	CreatedAt   string   `json:"createdAt"`
}

// apiCreateAPIKeyRequest is the API request to create an API key.
type apiCreateAPIKeyRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
	RateLimit   int      `json:"rateLimit,omitempty"`
}

// apiCreateAPIKeyResponse is the envelope identity returns on create: the key
// object nested under "apiKey", with the one-time secret alongside as "key".
// Read/Get instead returns a bare apiAPIKey. The provider previously decoded the
// create response as a bare apiAPIKey, so every nested field (id, name, scopes,
// expiresAt) came back empty and Terraform reported "provider produced
// inconsistent result after apply". Mirrors fm-cli's account.CreateAPIKeyResponse.
type apiCreateAPIKeyResponse struct {
	APIKey apiAPIKey `json:"apiKey"`
	Key    string    `json:"key"`
}

// apiUpdateAPIKeyRequest is the API request to update an API key.
type apiUpdateAPIKeyRequest struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	RateLimit   *int     `json:"rateLimit,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *APIKeyModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateAPIKeyRequest {
	req := apiCreateAPIKeyRequest{
		Name: m.Name.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	if !m.Scopes.IsNull() && !m.Scopes.IsUnknown() {
		var scopes []string
		diags.Append(m.Scopes.ElementsAs(ctx, &scopes, false)...)
		req.Scopes = scopes
	}

	if !m.ExpiresAt.IsNull() && !m.ExpiresAt.IsUnknown() {
		normalized, err := normalizeAPIKeyExpiry(m.ExpiresAt.ValueString())
		if err != nil {
			diags.AddAttributeError(path.Root("expires_at"), "Invalid expires_at", err.Error())
			return req
		}
		req.ExpiresAt = normalized
	}

	if !m.RateLimit.IsNull() && !m.RateLimit.IsUnknown() {
		req.RateLimit = int(m.RateLimit.ValueInt64())
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *APIKeyModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateAPIKeyRequest {
	req := apiUpdateAPIKeyRequest{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		desc := m.Description.ValueString()
		req.Description = &desc
	} else if m.Description.IsNull() {
		empty := ""
		req.Description = &empty
	}

	if !m.Scopes.IsNull() && !m.Scopes.IsUnknown() {
		var scopes []string
		diags.Append(m.Scopes.ElementsAs(ctx, &scopes, false)...)
		req.Scopes = scopes
	}

	if !m.RateLimit.IsNull() && !m.RateLimit.IsUnknown() {
		rl := int(m.RateLimit.ValueInt64())
		req.RateLimit = &rl
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
// The key field is only returned on create, so it is not set here.
func (m *APIKeyModel) fromAPI(ctx context.Context, key *apiAPIKey, diags *diag.Diagnostics) {
	m.ID = types.StringValue(key.ID)
	m.Name = types.StringValue(key.Name)
	m.KeyPrefix = types.StringValue(key.KeyPrefix)
	m.Status = types.StringValue(key.Status)
	m.CreatedAt = types.StringValue(key.CreatedAt)

	if key.Description != "" {
		m.Description = types.StringValue(key.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	switch {
	case key.ExpiresAt == "":
		m.ExpiresAt = types.StringNull()
	case !m.ExpiresAt.IsNull() && !m.ExpiresAt.IsUnknown() && sameExpiryInstant(m.ExpiresAt.ValueString(), key.ExpiresAt):
		// Preserve the user's config string (a bare date or a non-UTC offset):
		// it denotes the same instant the API returned, so keeping it makes
		// state == config and avoids a "provider produced inconsistent result"
		// error. On import (m.ExpiresAt null) this falls through to the API value.
	default:
		m.ExpiresAt = types.StringValue(key.ExpiresAt)
	}

	if key.RateLimit > 0 {
		m.RateLimit = types.Int64Value(int64(key.RateLimit))
	} else if m.RateLimit.IsNull() {
		m.RateLimit = types.Int64Null()
	} else {
		m.RateLimit = types.Int64Null()
	}

	// Scopes
	if len(key.Scopes) > 0 {
		scopeList, d := types.ListValueFrom(ctx, types.StringType, key.Scopes)
		diags.Append(d...)
		m.Scopes = scopeList
	} else if !m.Scopes.IsNull() {
		scopeList, d := types.ListValueFrom(ctx, types.StringType, []string{})
		diags.Append(d...)
		m.Scopes = scopeList
	} else {
		m.Scopes = types.ListNull(types.StringType)
	}

	// key field is NOT set here; it's only available on create and preserved from state.
}
