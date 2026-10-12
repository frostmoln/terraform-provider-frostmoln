// Package ssh_key implements the frostmoln_ssh_key Terraform resource.
package ssh_key

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// SSHKeyModel is the Terraform state model for an SSH key.
type SSHKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	CreatedAt   types.String `tfsdk:"created_at"`
	// Tags / TagsAll are the key's customer resource tags (internal/tftags).
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`
}

// sshKeyModelV0 is the v0 state shape (before the ID moved to the name, and
// before tags): UpgradeState decodes into it.
type sshKeyModelV0 struct {
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
	// Tags are the key's customer resource tags ({} when untagged).
	Tags map[string]string `json:"tags"`
}

// apiCreateSSHKeyRequest is the API request to create an SSH key.
type apiCreateSSHKeyRequest struct {
	Name      string `json:"name"`
	PublicKey string `json:"publicKey"`
	// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
	// merges the tenant's default tags on top (a key sent here wins).
	Tags map[string]string `json:"tags,omitempty"`
}

// apiUpdateTagsRequest is PATCH /sshkeys/{name}: `tags` is a key's only
// updatable field (any other is refused 400 field_not_updatable).
type apiUpdateTagsRequest struct {
	Tags *map[string]string `json:"tags"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SSHKeyModel) toCreateRequest() apiCreateSSHKeyRequest {
	return apiCreateSSHKeyRequest{
		Name:      m.Name.ValueString(),
		PublicKey: m.PublicKey.ValueString(),
	}
}

// fromAPI populates the Terraform model from an API response.
//
// Compute identifies SSH keys by name within a tenant — Read/Delete hit
// GET|DELETE /sshkeys/{name} and there is no get/delete-by-uuid route, so a
// uuid ID 404s on every Read (breaking import + destroy). The resource ID is
// therefore the key name, not the backend uuid.
func (m *SSHKeyModel) fromAPI(key *apiSSHKey) {
	m.ID = types.StringValue(key.Name)
	m.Name = types.StringValue(key.Name)

	// public_key is Required + RequiresReplace (config-owned, create-time). Keep
	// the configured value rather than overwriting it from the API, so a future
	// backend normalization (canonicalized key, trimmed comment) can't drift
	// state and force a spurious replace. On import there is no prior value, so
	// fall back to the API response.
	if m.PublicKey.IsNull() || m.PublicKey.IsUnknown() {
		m.PublicKey = types.StringValue(key.PublicKey)
	}

	m.Fingerprint = types.StringValue(key.Fingerprint)
	m.CreatedAt = types.StringValue(key.CreatedAt)
}

// tagsFromAPI renders the API's tags into `tags` (the keys the prior value names)
// and `tags_all` (every customer key). m.Tags must hold the prior value: the
// configuration on a create or update, the state on a refresh.
func (m *SSHKeyModel) tagsFromAPI(ctx context.Context, key *apiSSHKey, diags *diag.Diagnostics) {
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, tftags.OfferCustomerTags(key.Tags), m.Tags, diags)
}
