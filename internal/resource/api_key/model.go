// Package api_key implements the fm_api_key Terraform resource.
package api_key

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

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
		req.ExpiresAt = m.ExpiresAt.ValueString()
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

	if key.ExpiresAt != "" {
		m.ExpiresAt = types.StringValue(key.ExpiresAt)
	} else {
		m.ExpiresAt = types.StringNull()
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
