// Package kubernetes_cluster implements the frostmoln_kubernetes_cluster Terraform resource.
package kubernetes_cluster

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// KubernetesClusterModel is the Terraform state model for a managed Kubernetes cluster.
type KubernetesClusterModel struct {
	ID               types.String          `tfsdk:"id"`
	Name             types.String          `tfsdk:"name"`
	Version          types.String          `tfsdk:"version"`
	ControlPlaneTier types.String          `tfsdk:"control_plane_tier"`
	Region           types.String          `tfsdk:"region"`
	VPCID            types.String          `tfsdk:"vpc_id"`
	SubnetID         types.String          `tfsdk:"subnet_id"`
	FloatingIPID     types.String          `tfsdk:"floating_ip_id"`
	Addons           types.Set             `tfsdk:"addons"`
	InitialNodePool  *InitialNodePoolModel `tfsdk:"initial_node_pool"`
	Status           types.String          `tfsdk:"status"`
	HAEnabled        types.Bool            `tfsdk:"ha_enabled"`
	PodCIDR          types.String          `tfsdk:"pod_cidr"`
	ServiceCIDR      types.String          `tfsdk:"service_cidr"`
	Endpoint         types.String          `tfsdk:"endpoint"`
	LoadBalancerID   types.String          `tfsdk:"load_balancer_id"`
	FloatingIP       types.String          `tfsdk:"floating_ip"`
	CACertHash       types.String          `tfsdk:"ca_cert_hash"`
	Kubeconfig       types.String          `tfsdk:"kubeconfig"`
	CreatedAt        types.String          `tfsdk:"created_at"`
	UpdatedAt        types.String          `tfsdk:"updated_at"`
	TenantID         types.String          `tfsdk:"tenant_id"`
}

// InitialNodePoolModel is the Terraform state model for the cluster's initial
// node pool. The pool is created embedded in the cluster create request and is
// owned by the cluster resource; extra pools are separate
// frostmoln_kubernetes_node_pool resources.
type InitialNodePoolModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	FlavorID  types.String `tfsdk:"flavor_id"`
	NodeCount types.Int64  `tfsdk:"node_count"`
	Status    types.String `tfsdk:"status"`
}

// apiKubernetesCluster is the API representation of a managed Kubernetes
// cluster (kubernetes service domain.ManagedCluster).
type apiKubernetesCluster struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	TenantID          string `json:"tenantId,omitempty"`
	Status            string `json:"status"`
	KubernetesVersion string `json:"kubernetesVersion"`
	ControlPlaneTier  string `json:"controlPlaneTier"`
	HAEnabled         bool   `json:"haEnabled"`
	Region            string `json:"region"`
	VPCID             string `json:"vpcId"`
	SubnetID          string `json:"subnetId"`
	PodCIDR           string `json:"podCidr,omitempty"`
	ServiceCIDR       string `json:"serviceCidr,omitempty"`
	Endpoint          string `json:"endpoint,omitempty"`
	LoadBalancerID    string `json:"loadBalancerId,omitempty"`
	FloatingIP        string `json:"floatingIp,omitempty"`
	CACertHash        string `json:"caCertHash,omitempty"`
	// Addons is the set of cluster-addon catalog keys applied at creation. The
	// backend always includes it (may be empty) — it echoes exactly what was
	// applied. Addons are create-time only; they cannot change on an existing
	// cluster.
	Addons    []string `json:"addons"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
}

// apiNodePool is the API representation of a node pool (domain.NodePool).
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

// apiNodePoolList is the API response for listing a cluster's node pools.
type apiNodePoolList struct {
	NodePools []apiNodePool `json:"nodePools"`
}

// apiCreateNodePoolRequest is the embedded initial-node-pool part of a cluster
// create request.
type apiCreateNodePoolRequest struct {
	Name      string `json:"name,omitempty"`
	FlavorID  string `json:"flavorId"`
	NodeCount int    `json:"nodeCount"`
}

// apiCreateClusterRequest is the API request to create a managed Kubernetes cluster.
//
// Addons is a pointer-to-slice on purpose, to preserve the null-vs-empty
// distinction the server relies on: a nil pointer OMITS the field (server
// applies the catalog defaults), while a non-nil pointer to an empty slice
// serializes as `[]` (explicitly no addons). A plain []string with omitempty
// could not express "send an empty array" — omitempty drops a len-0 slice.
type apiCreateClusterRequest struct {
	Name              string                   `json:"name"`
	KubernetesVersion string                   `json:"kubernetesVersion,omitempty"`
	ControlPlaneTier  string                   `json:"controlPlaneTier,omitempty"`
	Region            string                   `json:"region,omitempty"`
	VPCID             string                   `json:"vpcId"`
	SubnetID          string                   `json:"subnetId"`
	FloatingIPID      string                   `json:"floatingIpId,omitempty"`
	Addons            *[]string                `json:"addons,omitempty"`
	InitialNodePool   apiCreateNodePoolRequest `json:"initialNodePool"`
}

// apiUpdateClusterRequest is the API request to update a cluster (name only in v1).
type apiUpdateClusterRequest struct {
	Name *string `json:"name,omitempty"`
}

// apiScaleNodePoolRequest is the API request to scale a node pool.
type apiScaleNodePoolRequest struct {
	NodeCount int `json:"nodeCount"`
}

// apiKubeconfig is the API response from the kubeconfig endpoint.
type apiKubeconfig struct {
	Endpoint   string `json:"endpoint"`
	Kubeconfig string `json:"kubeconfig"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *KubernetesClusterModel) toCreateRequest() apiCreateClusterRequest {
	req := apiCreateClusterRequest{
		Name:     m.Name.ValueString(),
		VPCID:    m.VPCID.ValueString(),
		SubnetID: m.SubnetID.ValueString(),
	}
	if !m.Version.IsNull() && !m.Version.IsUnknown() {
		req.KubernetesVersion = m.Version.ValueString()
	}
	if !m.ControlPlaneTier.IsNull() && !m.ControlPlaneTier.IsUnknown() {
		req.ControlPlaneTier = m.ControlPlaneTier.ValueString()
	}
	if !m.Region.IsNull() && !m.Region.IsUnknown() {
		req.Region = m.Region.ValueString()
	}
	if !m.FloatingIPID.IsNull() && !m.FloatingIPID.IsUnknown() {
		req.FloatingIPID = m.FloatingIPID.ValueString()
	}

	// Addons: only send the field when the practitioner set it (known value).
	// When unset (null/unknown, i.e. Computed-not-yet-resolved), leave req.Addons
	// nil so it is OMITTED and the server applies the catalog defaults. When set
	// — including an explicit empty set — send exactly the configured keys (a
	// non-nil pointer to a possibly-empty slice serializes as `[]`).
	if !m.Addons.IsNull() && !m.Addons.IsUnknown() {
		elems := m.Addons.Elements()
		addons := make([]string, 0, len(elems))
		for _, e := range elems {
			if s, ok := e.(types.String); ok {
				addons = append(addons, s.ValueString())
			}
		}
		req.Addons = &addons
	}

	pool := m.InitialNodePool
	req.InitialNodePool = apiCreateNodePoolRequest{
		FlavorID:  pool.FlavorID.ValueString(),
		NodeCount: int(pool.NodeCount.ValueInt64()),
	}
	if !pool.Name.IsNull() && !pool.Name.IsUnknown() {
		req.InitialNodePool.Name = pool.Name.ValueString()
	}

	return req
}

// fromAPI populates the Terraform model from an API cluster response.
//
// It deliberately does NOT touch:
//   - floating_ip_id: write-only on the API — the response exposes only the
//     resolved address (floatingIp); the configured value is preserved from
//     plan/state (see memory tf-provider-readback-preserve-create-time-attrs)
//   - kubeconfig: fetched from a separate endpoint
//   - initial_node_pool: a separate row, discovered via the node-pools list
func (m *KubernetesClusterModel) fromAPI(c *apiKubernetesCluster) {
	m.ID = types.StringValue(c.ID)
	m.Name = types.StringValue(c.Name)
	m.Version = stringOrNull(c.KubernetesVersion)
	m.ControlPlaneTier = stringOrNull(c.ControlPlaneTier)
	m.Region = stringOrNull(c.Region)
	m.VPCID = types.StringValue(c.VPCID)
	m.SubnetID = types.StringValue(c.SubnetID)
	m.Status = types.StringValue(c.Status)
	m.HAEnabled = types.BoolValue(c.HAEnabled)
	m.PodCIDR = stringOrNull(c.PodCIDR)
	m.ServiceCIDR = stringOrNull(c.ServiceCIDR)
	m.Endpoint = stringOrNull(c.Endpoint)
	m.LoadBalancerID = stringOrNull(c.LoadBalancerID)
	m.FloatingIP = stringOrNull(c.FloatingIP)
	m.CACertHash = stringOrNull(c.CACertHash)
	// The backend always echoes the applied addon set (possibly empty). Map it
	// to a concrete (never null) set: an empty response array yields an empty
	// set, which matches an explicit empty-set config and, via Computed +
	// UseStateForUnknown, fills state cleanly when addons was left unset.
	m.Addons = stringSliceToSet(c.Addons)
	m.CreatedAt = types.StringValue(c.CreatedAt)
	m.UpdatedAt = stringOrNull(c.UpdatedAt)
	m.TenantID = stringOrNull(c.TenantID)
}

// setInitialNodePool fills the nested initial_node_pool block from a node-pool
// API response.
func (m *KubernetesClusterModel) setInitialNodePool(p *apiNodePool) {
	m.InitialNodePool = &InitialNodePoolModel{
		ID:        types.StringValue(p.ID),
		Name:      types.StringValue(p.Name),
		FlavorID:  types.StringValue(p.FlavorID),
		NodeCount: types.Int64Value(int64(p.NodeCount)),
		Status:    types.StringValue(p.Status),
	}
}

func stringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// stringSliceToSet builds a Terraform set of strings. A nil OR empty slice
// yields an empty (non-null) set — the backend always returns the addons array,
// so state should never carry a null addons set. Building a set from plain
// string values cannot fail, so SetValueMust is safe here.
func stringSliceToSet(items []string) types.Set {
	elems := make([]attr.Value, 0, len(items))
	for _, s := range items {
		elems = append(elems, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, elems)
}
