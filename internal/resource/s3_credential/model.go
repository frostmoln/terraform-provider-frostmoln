// Package s3_credential implements the fm_s3_credential Terraform resource.
package s3_credential

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// S3CredentialModel is the Terraform state model for an S3 credential.
type S3CredentialModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	SecretAccessKey types.String `tfsdk:"secret_access_key"`
	Status          types.String `tfsdk:"status"`
	CreatedAt       types.String `tfsdk:"created_at"`

	// Scoping (ADR-0030). Empty/unset = unrestricted on that axis. Immutable
	// (RequiresReplace) — a credential's scope cannot be edited in place.
	AllowedBuckets types.List `tfsdk:"allowed_buckets"`
	AllowedActions types.List `tfsdk:"allowed_actions"`
	IPWhitelist    types.List `tfsdk:"ip_whitelist"`
}

// apiS3Credential is the API representation of an S3 credential.
type apiS3Credential struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	AccessKey       string   `json:"accessKey,omitempty"`
	SecretAccessKey string   `json:"secretAccessKey,omitempty"`
	Status          string   `json:"status"`
	AllowedBuckets  []string `json:"allowedBuckets,omitempty"`
	AllowedActions  []string `json:"allowedActions,omitempty"`
	IPWhitelist     []string `json:"ipWhitelist,omitempty"`
	CreatedAt       string   `json:"createdAt"`
}

// apiCreateS3CredentialRequest is the API request to create an S3 credential.
type apiCreateS3CredentialRequest struct {
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	AllowedBuckets []string `json:"allowedBuckets,omitempty"`
	AllowedActions []string `json:"allowedActions,omitempty"`
	IPWhitelist    []string `json:"ipWhitelist,omitempty"`
}

// apiS3CredentialList is the API response for listing S3 credentials.
type apiS3CredentialList struct {
	Credentials []apiS3Credential `json:"credentials"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *S3CredentialModel) toCreateRequest(ctx context.Context) (apiCreateS3CredentialRequest, diag.Diagnostics) {
	req := apiCreateS3CredentialRequest{
		Name: m.Name.ValueString(),
	}
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	var diags diag.Diagnostics
	if d := listToStrings(ctx, m.AllowedBuckets, &req.AllowedBuckets); d.HasError() {
		diags.Append(d...)
	}
	if d := listToStrings(ctx, m.AllowedActions, &req.AllowedActions); d.HasError() {
		diags.Append(d...)
	}
	if d := listToStrings(ctx, m.IPWhitelist, &req.IPWhitelist); d.HasError() {
		diags.Append(d...)
	}
	return req, diags
}

// fromAPI populates the Terraform model from an API response.
func (m *S3CredentialModel) fromAPI(ctx context.Context, cred *apiS3Credential) diag.Diagnostics {
	m.ID = types.StringValue(cred.ID)
	m.Name = types.StringValue(cred.Name)
	m.Status = types.StringValue(cred.Status)
	m.CreatedAt = types.StringValue(cred.CreatedAt)

	if cred.Description != "" {
		m.Description = types.StringValue(cred.Description)
	} else if m.Description.IsNull() {
		// Keep null if it was null and API returns empty.
		m.Description = types.StringNull()
	}

	if cred.SecretAccessKey != "" {
		m.SecretAccessKey = types.StringValue(cred.SecretAccessKey)
	}
	// If SecretAccessKey is empty (read after create), don't overwrite -
	// UseStateForUnknown plan modifier preserves the state value.

	var diags diag.Diagnostics
	if d := stringsToList(ctx, cred.AllowedBuckets, &m.AllowedBuckets); d.HasError() {
		diags.Append(d...)
	}
	if d := stringsToList(ctx, cred.AllowedActions, &m.AllowedActions); d.HasError() {
		diags.Append(d...)
	}
	if d := stringsToList(ctx, cred.IPWhitelist, &m.IPWhitelist); d.HasError() {
		diags.Append(d...)
	}
	return diags
}

// listToStrings converts an optional Terraform string list into a Go slice,
// leaving dst nil (so it is omitted from the request) when the list is unset.
func listToStrings(ctx context.Context, l types.List, dst *[]string) diag.Diagnostics {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(l.Elements()))
	diags := l.ElementsAs(ctx, &out, false)
	if !diags.HasError() {
		*dst = out
	}
	return diags
}

// stringsToList sets dst from a Go slice. A non-empty slice becomes a known
// list; an empty/absent value preserves a null only if dst was already null
// (so unrestricted credentials don't show perpetual null-vs-[] drift).
func stringsToList(ctx context.Context, in []string, dst *types.List) diag.Diagnostics {
	if len(in) > 0 {
		l, diags := types.ListValueFrom(ctx, types.StringType, in)
		if !diags.HasError() {
			*dst = l
		}
		return diags
	}
	if dst.IsNull() || dst.IsUnknown() {
		*dst = types.ListNull(types.StringType)
	}
	return nil
}
