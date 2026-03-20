// Package ssh_key implements the fm_ssh_key Terraform resource.
package ssh_key

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// SSHKeyModel is the Terraform state model for an SSH key.
type SSHKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiSSHKey is the API representation of an SSH key.
type apiSSHKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"createdAt"`
}

// apiCreateSSHKeyRequest is the API request to create an SSH key.
type apiCreateSSHKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
}

// apiSSHKeyList is the API response for listing SSH keys.
type apiSSHKeyList struct {
	SSHKeys []apiSSHKey `json:"keys"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SSHKeyModel) toCreateRequest() apiCreateSSHKeyRequest {
	return apiCreateSSHKeyRequest{
		Name:      m.Name.ValueString(),
		PublicKey: m.PublicKey.ValueString(),
	}
}

// fromAPI populates the Terraform model from an API response.
func (m *SSHKeyModel) fromAPI(key *apiSSHKey) {
	m.ID = types.StringValue(key.ID)
	m.Name = types.StringValue(key.Name)
	m.PublicKey = types.StringValue(key.PublicKey)
	m.Fingerprint = types.StringValue(key.Fingerprint)
	m.CreatedAt = types.StringValue(key.CreatedAt)
}
