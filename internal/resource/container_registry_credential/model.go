// Package container_registry_credential implements the
// frostmoln_container_registry_credential Terraform resource.
package container_registry_credential

import (
	"context"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ContainerRegistryCredentialModel is the Terraform state model for a registry
// credential.
type ContainerRegistryCredentialModel struct {
	// ID is the credential's numeric id, held as a string because Terraform
	// resource identifiers are strings platform-wide.
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Capability types.String `tfsdk:"capability"`
	Username   types.String `tfsdk:"username"`
	Secret     types.String `tfsdk:"secret"`
	Disabled   types.Bool   `tfsdk:"disabled"`
	Endpoint   types.String `tfsdk:"endpoint"`
	Namespaces types.List   `tfsdk:"namespaces"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

// apiCredential is the API representation of a registry credential.
//
// ID is an int64, not a UUID: it is the underlying robot account's numeric id,
// and it is what the member routes take as their path segment.
//
// Secret is present in a CREATE response and guaranteed absent everywhere else —
// there is no retrievable copy and no rotation endpoint.
type apiCredential struct {
	ID         int64    `json:"id"`
	Username   string   `json:"username"`
	Name       string   `json:"name"`
	Capability string   `json:"capability"`
	Disabled   bool     `json:"disabled"`
	Endpoint   string   `json:"endpoint"`
	Namespaces []string `json:"namespaces"`
	CreatedAt  string   `json:"createdAt"`
	Secret     string   `json:"secret,omitempty"`
}

// apiCreateCredentialRequest is the API request to mint a credential.
type apiCreateCredentialRequest struct {
	Name       string `json:"name"`
	Capability string `json:"capability"`
}

// fromAPI populates the Terraform model from an API response.
//
// Secret is NOT written here when the response carries none: reads never return
// it, and blanking it would destroy the only copy that exists. The caller
// carries it across from state instead.
func (m *ContainerRegistryCredentialModel) fromAPI(ctx context.Context, cred *apiCredential) diag.Diagnostics {
	m.ID = types.StringValue(strconv.FormatInt(cred.ID, 10))
	m.Name = types.StringValue(cred.Name)
	m.Capability = types.StringValue(cred.Capability)
	m.Username = types.StringValue(cred.Username)
	m.Disabled = types.BoolValue(cred.Disabled)
	m.Endpoint = types.StringValue(cred.Endpoint)
	m.CreatedAt = types.StringValue(cred.CreatedAt)

	if cred.Secret != "" {
		m.Secret = types.StringValue(cred.Secret)
	}

	// A credential always reaches at least one namespace, so an empty list is a
	// real (if surprising) answer rather than "unset" — render it as an empty
	// list, never null, so it does not read as unknown on the next plan.
	namespaces := cred.Namespaces
	if namespaces == nil {
		namespaces = []string{}
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, namespaces)
	if !diags.HasError() {
		m.Namespaces = list
	}
	return diags
}
