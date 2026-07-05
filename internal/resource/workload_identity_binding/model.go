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
type apiCreateWorkloadIdentityBindingRequest struct {
	ClusterID      string   `json:"clusterId"`
	Namespace      string   `json:"namespace"`
	ServiceAccount string   `json:"serviceAccount"`
	Scopes         []string `json:"scopes"`
}

// apiUpdateWorkloadIdentityBindingRequest replaces the binding's scopes (PUT).
type apiUpdateWorkloadIdentityBindingRequest struct {
	Scopes []string `json:"scopes"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *WorkloadIdentityBindingModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateWorkloadIdentityBindingRequest {
	req := apiCreateWorkloadIdentityBindingRequest{
		ClusterID:      m.ClusterID.ValueString(),
		Namespace:      m.Namespace.ValueString(),
		ServiceAccount: m.ServiceAccount.ValueString(),
	}
	diags.Append(m.Scopes.ElementsAs(ctx, &req.Scopes, false)...)
	return req
}

// toUpdateRequest converts the Terraform model to an API scope-replacement request.
func (m *WorkloadIdentityBindingModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateWorkloadIdentityBindingRequest {
	req := apiUpdateWorkloadIdentityBindingRequest{}
	diags.Append(m.Scopes.ElementsAs(ctx, &req.Scopes, false)...)
	return req
}

// fromAPI populates the Terraform model from an API response. Scopes come from
// the server verbatim (it preserves the submitted order), so state matches the
// plan without a spurious diff.
func (m *WorkloadIdentityBindingModel) fromAPI(ctx context.Context, b *apiWorkloadIdentityBinding, diags *diag.Diagnostics) {
	m.ID = types.StringValue(b.ID)
	m.ClusterID = types.StringValue(b.ClusterID)
	m.TenantID = types.StringValue(b.TenantID)
	m.Namespace = types.StringValue(b.Namespace)
	m.ServiceAccount = types.StringValue(b.ServiceAccount)
	m.CreatedAt = types.StringValue(b.CreatedAt)
	m.UpdatedAt = types.StringValue(b.UpdatedAt)

	scopeList, d := types.ListValueFrom(ctx, types.StringType, b.Scopes)
	diags.Append(d...)
	m.Scopes = scopeList
}
