// Package workload_identity_binding implements the frostmoln_workload_identity_binding
// Terraform resource (ADR-0095).
package workload_identity_binding

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// WorkloadIdentityBindingModel is the Terraform state model for a binding.
type WorkloadIdentityBindingModel struct {
	ID             types.String `tfsdk:"id"`
	ClusterID      types.String `tfsdk:"cluster_id"`
	TenantID       types.String `tfsdk:"tenant_id"`
	Namespace      types.String `tfsdk:"namespace"`
	ServiceAccount types.String `tfsdk:"service_account"`
	Scopes         types.List   `tfsdk:"scopes"`
	CreatedAt      types.String `tfsdk:"created_at"`
	UpdatedAt      types.String `tfsdk:"updated_at"`
}

// apiWorkloadIdentityBinding is the API representation (camelCase, ADR-0095).
type apiWorkloadIdentityBinding struct {
	ID             string   `json:"id"`
	ClusterID      string   `json:"clusterId"`
	TenantID       string   `json:"tenantId"`
	Namespace      string   `json:"namespace"`
	ServiceAccount string   `json:"serviceAccount"`
	Scopes         []string `json:"scopes"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

// apiCreateWorkloadIdentityBindingRequest is the create body. The tenant is
// server-set from the auth context, so it is never sent.
//
// `scopes` is omitempty on purpose. identity's OpenAPI declares it as a plain
// array with no `nullable: true`, so `"scopes": null` is not a valid instance of
// the published contract even though Gin's decoder happens to accept it. The
// spec's sanctioned spellings are omitting or emptying the field, and the server
// treats an omitted key as the empty set on BOTH create and update.
type apiCreateWorkloadIdentityBindingRequest struct {
	ClusterID      string   `json:"clusterId"`
	Namespace      string   `json:"namespace"`
	ServiceAccount string   `json:"serviceAccount"`
	Scopes         []string `json:"scopes,omitempty"`
}

// apiUpdateWorkloadIdentityBindingRequest replaces the binding's scopes (PUT).
// Omitting the field is a full replacement with the empty set, not a no-op — see
// the create request's note on why it is omitempty rather than an explicit null.
type apiUpdateWorkloadIdentityBindingRequest struct {
	Scopes []string `json:"scopes,omitempty"`
}

// scopeStrings reads the model's scopes as a plain slice. `scopes` is Optional
// (ADR-0102: a policy-granted binding carries none), so a NULL list is a
// legitimate value, not an error — ElementsAs would append an "unhandled null
// value" diagnostic for it. A nil slice is omitted from the request body, which
// identity reads as the empty set on both create and update.
func (m *WorkloadIdentityBindingModel) scopeStrings(ctx context.Context, diags *diag.Diagnostics) []string {
	if m.Scopes.IsNull() {
		return nil
	}
	// UNKNOWN is unreachable: `scopes` is Optional and NOT Computed, so Terraform
	// resolves it before apply. Refuse rather than fold it into "no scopes" — if
	// the attribute ever gains Computed (see CLAUDE.md's Optional+Computed
	// section), silently clearing a workload's grant is the wrong default.
	if m.Scopes.IsUnknown() {
		diags.AddError(
			"Unknown workload identity binding scopes",
			"The scopes value was still unknown when the request was built, which should not be "+
				"possible for a non-computed attribute. Please report this as a provider bug.",
		)
		return nil
	}
	var scopes []string
	diags.Append(m.Scopes.ElementsAs(ctx, &scopes, false)...)
	return scopes
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *WorkloadIdentityBindingModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateWorkloadIdentityBindingRequest {
	return apiCreateWorkloadIdentityBindingRequest{
		ClusterID:      m.ClusterID.ValueString(),
		Namespace:      m.Namespace.ValueString(),
		ServiceAccount: m.ServiceAccount.ValueString(),
		Scopes:         m.scopeStrings(ctx, diags),
	}
}

// toUpdateRequest converts the Terraform model to an API scope-replacement
// request. PUT is a full replacement: dropping `scopes` from the config sends
// null, which empties the binding's flat grant — identity rejects that with
// BINDING_WOULD_HAVE_NO_GRANT when it was the last one (ADR-0102).
func (m *WorkloadIdentityBindingModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateWorkloadIdentityBindingRequest {
	return apiUpdateWorkloadIdentityBindingRequest{Scopes: m.scopeStrings(ctx, diags)}
}

// fromAPI populates the Terraform model from an API response. Scopes come from
// the server verbatim (it preserves the submitted order), so state matches the
// plan without a spurious diff.
//
// A scope-less binding is normalized to a NULL list whether the server echoes
// `null` or `[]`: the config spelling for "no scopes" is an omitted attribute
// (an empty list literal is rejected by the schema validator), so an empty list
// in state would be a diff Terraform can never converge.
func (m *WorkloadIdentityBindingModel) fromAPI(ctx context.Context, b *apiWorkloadIdentityBinding, diags *diag.Diagnostics) {
	m.ID = types.StringValue(b.ID)
	m.ClusterID = types.StringValue(b.ClusterID)
	m.TenantID = types.StringValue(b.TenantID)
	m.Namespace = types.StringValue(b.Namespace)
	m.ServiceAccount = types.StringValue(b.ServiceAccount)
	m.CreatedAt = types.StringValue(b.CreatedAt)
	m.UpdatedAt = types.StringValue(b.UpdatedAt)

	if len(b.Scopes) == 0 {
		m.Scopes = types.ListNull(types.StringType)
		return
	}
	scopeList, d := types.ListValueFrom(ctx, types.StringType, b.Scopes)
	diags.Append(d...)
	m.Scopes = scopeList
}
