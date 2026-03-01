// Package s3_credential implements the fm_s3_credential Terraform resource.
package s3_credential

import (
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
}

// apiS3Credential is the API representation of an S3 credential.
type apiS3Credential struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	AccessKey       string `json:"accessKey,omitempty"`
	SecretAccessKey string `json:"secretAccessKey,omitempty"`
	Status          string `json:"status"`
	CreatedAt       string `json:"createdAt"`
}

// apiCreateS3CredentialRequest is the API request to create an S3 credential.
type apiCreateS3CredentialRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// apiS3CredentialList is the API response for listing S3 credentials.
type apiS3CredentialList struct {
	Credentials []apiS3Credential `json:"credentials"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *S3CredentialModel) toCreateRequest() apiCreateS3CredentialRequest {
	req := apiCreateS3CredentialRequest{
		Name: m.Name.ValueString(),
	}
	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}
	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *S3CredentialModel) fromAPI(cred *apiS3Credential) {
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
}
