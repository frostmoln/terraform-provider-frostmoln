// Package secret implements the frostmoln_secret Terraform resource.
package secret

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SecretModel is the Terraform state model for a secret.
type SecretModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	SecretValue        types.String `tfsdk:"secret_value"`
	ContentType        types.String `tfsdk:"content_type"`
	Tags               types.Map    `tfsdk:"tags"`
	MaxVersions        types.Int64  `tfsdk:"max_versions"`
	RecoveryWindowDays types.Int64  `tfsdk:"recovery_window_days"`
	CurrentVersion     types.Int64  `tfsdk:"current_version"`
	Status             types.String `tfsdk:"status"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
}

// apiSecret is the API representation of a secret.
type apiSecret struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	SecretValue        string            `json:"secretValue,omitempty"`
	ContentType        string            `json:"contentType"`
	Tags               map[string]string `json:"tags,omitempty"`
	MaxVersions        int               `json:"maxVersions"`
	RecoveryWindowDays int               `json:"recoveryWindowDays"`
	CurrentVersion     int               `json:"currentVersion"`
	Status             string            `json:"status"`
	CreatedAt          string            `json:"createdAt"`
	UpdatedAt          string            `json:"updatedAt,omitempty"`
}

// apiCreateSecretRequest is the API request to create a secret.
type apiCreateSecretRequest struct {
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	SecretValue        string            `json:"secretValue"`
	ContentType        string            `json:"contentType,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	MaxVersions        int               `json:"maxVersions,omitempty"`
	RecoveryWindowDays int               `json:"recoveryWindowDays,omitempty"`
}

// apiUpdateSecretRequest is the API request to update a secret.
type apiUpdateSecretRequest struct {
	Description        *string           `json:"description,omitempty"`
	SecretValue        *string           `json:"secretValue,omitempty"`
	ContentType        *string           `json:"contentType,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
	MaxVersions        *int              `json:"maxVersions,omitempty"`
	RecoveryWindowDays *int              `json:"recoveryWindowDays,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SecretModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateSecretRequest {
	req := apiCreateSecretRequest{
		Name:        m.Name.ValueString(),
		SecretValue: m.SecretValue.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	if !m.ContentType.IsNull() && !m.ContentType.IsUnknown() {
		req.ContentType = m.ContentType.ValueString()
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	if !m.MaxVersions.IsNull() && !m.MaxVersions.IsUnknown() {
		req.MaxVersions = int(m.MaxVersions.ValueInt64())
	}

	if !m.RecoveryWindowDays.IsNull() && !m.RecoveryWindowDays.IsUnknown() {
		req.RecoveryWindowDays = int(m.RecoveryWindowDays.ValueInt64())
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *SecretModel) toUpdateRequest(ctx context.Context, state *SecretModel, diags *diag.Diagnostics) apiUpdateSecretRequest {
	req := apiUpdateSecretRequest{}

	if !m.Description.Equal(state.Description) {
		if m.Description.IsNull() {
			empty := ""
			req.Description = &empty
		} else {
			v := m.Description.ValueString()
			req.Description = &v
		}
	}

	if !m.SecretValue.Equal(state.SecretValue) {
		v := m.SecretValue.ValueString()
		req.SecretValue = &v
	}

	if !m.ContentType.Equal(state.ContentType) {
		v := m.ContentType.ValueString()
		req.ContentType = &v
	}

	if !m.Tags.Equal(state.Tags) {
		if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
			tags := make(map[string]string)
			diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
			req.Tags = tags
		}
	}

	if !m.MaxVersions.Equal(state.MaxVersions) {
		v := int(m.MaxVersions.ValueInt64())
		req.MaxVersions = &v
	}

	if !m.RecoveryWindowDays.Equal(state.RecoveryWindowDays) {
		v := int(m.RecoveryWindowDays.ValueInt64())
		req.RecoveryWindowDays = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *SecretModel) fromAPI(ctx context.Context, s *apiSecret, diags *diag.Diagnostics) {
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Name)
	m.ContentType = types.StringValue(s.ContentType)
	m.MaxVersions = types.Int64Value(int64(s.MaxVersions))
	m.RecoveryWindowDays = types.Int64Value(int64(s.RecoveryWindowDays))
	m.CurrentVersion = types.Int64Value(int64(s.CurrentVersion))
	m.Status = types.StringValue(s.Status)
	m.CreatedAt = types.StringValue(s.CreatedAt)

	if s.Description != "" {
		m.Description = types.StringValue(s.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	// Only set secret_value from API if it was returned (create/read-with-value).
	// Preserve the plan value otherwise.
	if s.SecretValue != "" {
		m.SecretValue = types.StringValue(s.SecretValue)
	}

	if s.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(s.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	if len(s.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, s.Tags)
		diags.Append(d...)
		m.Tags = tagsMap
	} else if m.Tags.IsNull() {
		m.Tags = types.MapNull(types.StringType)
	} else {
		m.Tags = types.MapNull(types.StringType)
	}
}
