// Package kubernetes_node_pool implements the frostmoln_kubernetes_node_pool
// Terraform resource: an additional (non-initial) node pool on a managed
// Kubernetes cluster. The cluster's initial pool is owned by the
// frostmoln_kubernetes_cluster resource and is refused here.
package kubernetes_node_pool

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// KubernetesNodePoolModel is the Terraform state model for a standalone node pool.
type KubernetesNodePoolModel struct {
	ID        types.String `tfsdk:"id"`
	ClusterID types.String `tfsdk:"cluster_id"`
	Name      types.String `tfsdk:"name"`
	FlavorID  types.String `tfsdk:"flavor_id"`
	NodeCount types.Int64  `tfsdk:"node_count"`
	Status    types.String `tfsdk:"status"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

// apiNodePool is the API representation of a node pool (kubernetes service
// domain.NodePool). isInitial is present in responses though absent from the
// OpenAPI spec (existing provider pattern: hand-written structs, tolerate).
type apiNodePool struct {
	ID        string `json:"id"`
	ClusterID string `json:"clusterId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	FlavorID  string `json:"flavorId"`
	NodeCount int    `json:"nodeCount"`
	IsInitial bool   `json:"isInitial"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// apiCreateNodePoolRequest is the API request to create a standalone node pool.
// Name is optional: omitted, the backend generates "pool-<8 hex>".
type apiCreateNodePoolRequest struct {
	Name      string `json:"name,omitempty"`
	FlavorID  string `json:"flavorId"`
	NodeCount int    `json:"nodeCount"`
}

// apiScaleNodePoolRequest is the API request to scale a node pool.
type apiScaleNodePoolRequest struct {
	NodeCount int `json:"nodeCount"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *KubernetesNodePoolModel) toCreateRequest() apiCreateNodePoolRequest {
	req := apiCreateNodePoolRequest{
		FlavorID:  m.FlavorID.ValueString(),
		NodeCount: int(m.NodeCount.ValueInt64()),
	}
	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		req.Name = m.Name.ValueString()
	}
	return req
}

// fromAPI populates the Terraform model from an API node-pool response. The
// backend always echoes clusterId, but the state value is kept as a fallback
// so a partial response can never blank the composite identity.
func (m *KubernetesNodePoolModel) fromAPI(p *apiNodePool) {
	m.ID = types.StringValue(p.ID)
	if p.ClusterID != "" {
		m.ClusterID = types.StringValue(p.ClusterID)
	}
	m.Name = types.StringValue(p.Name)
	m.FlavorID = types.StringValue(p.FlavorID)
	m.NodeCount = types.Int64Value(int64(p.NodeCount))
	m.Status = types.StringValue(p.Status)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = stringOrNull(p.UpdatedAt)
}

func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
