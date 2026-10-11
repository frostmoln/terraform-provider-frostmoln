// Package kubernetes_node_pool implements the frostmoln_kubernetes_node_pool
// Terraform resource: an additional (non-initial) node pool on a managed
// Kubernetes cluster. The cluster's initial pool is owned by the
// frostmoln_kubernetes_cluster resource and is refused here.
package kubernetes_node_pool

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// KubernetesNodePoolModel is the Terraform state model for a standalone node pool.
type KubernetesNodePoolModel struct {
	ID            types.String `tfsdk:"id"`
	ClusterID     types.String `tfsdk:"cluster_id"`
	Name          types.String `tfsdk:"name"`
	FlavorID      types.String `tfsdk:"flavor_id"`
	NodeCount     types.Int64  `tfsdk:"node_count"`
	Status        types.String `tfsdk:"status"`
	StatusReason  types.String `tfsdk:"status_reason"`
	StatusMessage types.String `tfsdk:"status_message"`
	FailedStep    types.String `tfsdk:"failed_step"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
	// Tags / TagsAll are the pool's customer resource tags (internal/tftags) — not
	// Kubernetes node labels.
	Tags    types.Map `tfsdk:"tags"`
	TagsAll types.Map `tfsdk:"tags_all"`

	// Timeouts carries the customer-tunable wait budgets; a nil pointer is an
	// absent block, which resolves to the resource's hardcoded defaults.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

// apiNodePool is the API representation of a node pool (kubernetes service
// domain.NodePool). isInitial is present in responses though absent from the
// OpenAPI spec (existing provider pattern: hand-written structs, tolerate).
type apiNodePool struct {
	ID        string `json:"id"`
	ClusterID string `json:"clusterId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	// StatusReason, StatusMessage and FailedStep say why the row failed; the
	// backend sends them only when status is "error".
	StatusReason  string `json:"statusReason,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
	FailedStep    string `json:"failedStep,omitempty"`
	FlavorID      string `json:"flavorId"`
	NodeCount     int    `json:"nodeCount"`
	IsInitial     bool   `json:"isInitial"`
	// Tags are the pool's customer resource tags; the backend always sends an
	// object ({} when untagged).
	Tags      map[string]string `json:"tags"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
}

// apiCreateNodePoolRequest is the API request to create a standalone node pool.
// Name is optional: omitted, the backend generates "pool-<8 hex>".
//
// Tags is default_tags ∪ tags (tftags.ForCreate), omitted when empty; the API
// merges the tenant's default tags in.
type apiCreateNodePoolRequest struct {
	Name      string            `json:"name,omitempty"`
	FlavorID  string            `json:"flavorId"`
	NodeCount int               `json:"nodeCount"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// apiUpdateNodePoolRequest is PATCH .../node-pools/{poolId}: only the fields
// present change. This resource sends it for tags alone (node_count goes through
// POST .../scale). Tags is a POINTER: nil omits the field (keep), a pointer to an
// EMPTY map sends `"tags":{}` (clear) — omitempty on a plain map would drop that.
// A tags-only update touches no node and starts no provisioning work.
type apiUpdateNodePoolRequest struct {
	Tags *map[string]string `json:"tags,omitempty"`
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
	m.StatusReason = stringOrNull(p.StatusReason)
	m.StatusMessage = stringOrNull(p.StatusMessage)
	m.FailedStep = stringOrNull(p.FailedStep)
	m.CreatedAt = types.StringValue(p.CreatedAt)
	m.UpdatedAt = stringOrNull(p.UpdatedAt)
}

// tagsFromAPI renders the API's tags into `tags` (the keys the prior value names)
// and `tags_all` (every key). m.Tags must hold the prior value: the configuration
// on a create or update, the state on a refresh.
func (m *KubernetesNodePoolModel) tagsFromAPI(ctx context.Context, p *apiNodePool, diags *diag.Diagnostics) {
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, customerTags(p), m.Tags, diags)
}

// customerTags is the tag set the platform holds on a pool, minus the namespace
// the offer reserves; the read-back and the update's pre-write read both use it.
func customerTags(p *apiNodePool) map[string]string {
	return reservedmeta.FilterOffer(p.Tags)
}

func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
