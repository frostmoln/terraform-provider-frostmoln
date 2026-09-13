// Package security_group implements the fm_security_group Terraform resource.
package security_group

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// SecurityGroupModel is the Terraform state model for a security group.
type SecurityGroupModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Description         types.String `tfsdk:"description"`
	VPCID               types.String `tfsdk:"vpc_id"`
	Tags                types.Map    `tfsdk:"tags"`
	TagsAll             types.Map    `tfsdk:"tags_all"`
	IsDefault           types.Bool   `tfsdk:"is_default"`
	DeleteDefaultEgress types.Bool   `tfsdk:"delete_default_egress"`
	CreatedAt           types.String `tfsdk:"created_at"`

	// Timeouts carries the customer-tunable wait budgets; a nil pointer is an
	// absent block, which resolves to the resource's hardcoded defaults.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

// apiSecurityGroup is the API representation of a security group.
type apiSecurityGroup struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	VPCID       string            `json:"vpcId,omitempty"`
	IsDefault   bool              `json:"isDefault"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"createdAt"`
}

// apiSecurityGroupList is the family listing the create-timeout adoption sweep
// reads (GET /security-groups → {"securityGroups":[…]}); the list hides
// offer-internal groups server-side, which are never this resource's.
type apiSecurityGroupList struct {
	Items []apiSecurityGroup `json:"securityGroups"`
}

// apiSecurityGroupRule is the read model of a rule carried by a group. It
// exists only to find the platform-injected default egress rules for
// delete_default_egress — managing rules is frostmoln_security_group_rule's
// job, never this resource's. The wire tags match that resource's model: the
// network service serializes the remote group as `remoteSecurityGroupId` and
// the remote CIDR as `remoteCidr`.
type apiSecurityGroupRule struct {
	ID            string `json:"id"`
	Direction     string `json:"direction"`
	Protocol      string `json:"protocol"`
	RemoteCIDR    string `json:"remoteCidr"`
	RemoteGroupID string `json:"remoteSecurityGroupId"`
}

// apiSecurityGroupWithRules is the group read with its rules embedded — rules
// are not directly listable, only reachable through the parent group.
type apiSecurityGroupWithRules struct {
	Rules []apiSecurityGroupRule `json:"rules"`
}

// apiCreateSecurityGroupRequest is the API request to create a security group.
type apiCreateSecurityGroupRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	VPCID       string            `json:"vpcId,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// apiUpdateSecurityGroupRequest is the API request to update a security group.
type apiUpdateSecurityGroupRequest struct {
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	Tags        map[string]string `json:"tags"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *SecurityGroupModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateSecurityGroupRequest {
	req := apiCreateSecurityGroupRequest{
		Name: m.Name.ValueString(),
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		req.Description = m.Description.ValueString()
	}

	if !m.VPCID.IsNull() && !m.VPCID.IsUnknown() {
		req.VPCID = m.VPCID.ValueString()
	}

	// Tags are set by Create, which merges the provider's default_tags into
	// them (tftags.ForCreate).

	return req
}

// toUpdateRequest converts the Terraform model to an API update request. Tags
// are set by Update, which merges them with the provider's default_tags and
// the keys it does not manage (tftags.ForUpdate).
func (m *SecurityGroupModel) toUpdateRequest() apiUpdateSecurityGroupRequest {
	req := apiUpdateSecurityGroupRequest{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}

	if !m.Description.IsNull() && !m.Description.IsUnknown() {
		desc := m.Description.ValueString()
		req.Description = &desc
	} else if m.Description.IsNull() {
		empty := ""
		req.Description = &empty
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *SecurityGroupModel) fromAPI(ctx context.Context, sg *apiSecurityGroup, diags *diag.Diagnostics) {
	m.ID = types.StringValue(sg.ID)
	m.Name = types.StringValue(sg.Name)
	m.IsDefault = types.BoolValue(sg.IsDefault)
	m.CreatedAt = types.StringValue(sg.CreatedAt)

	if sg.Description != "" {
		m.Description = types.StringValue(sg.Description)
	} else if m.Description.IsNull() {
		m.Description = types.StringNull()
	} else {
		m.Description = types.StringValue("")
	}

	if sg.VPCID != "" {
		m.VPCID = types.StringValue(sg.VPCID)
	} else {
		m.VPCID = types.StringNull()
	}

	// Platform-owned frostmoln_* keys are filtered first: network refuses them on
	// every customer write and carries them across every tag update
	// (nlmeta.MergePlatformOwnedTags), so no config can converge on one.
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, sg.customerTags(), m.Tags, diags)

	// delete_default_egress is create-time behaviour the API knows nothing
	// about, so state carries it. A group imported (or created before the
	// attribute existed) has no value; adopt the false default here, which is
	// what its next plan resolves to anyway — this is what lets an import of
	// such a group plan empty.
	if m.DeleteDefaultEgress.IsNull() {
		m.DeleteDefaultEgress = types.BoolValue(false)
	}
}

// customerTags is the tag set the platform holds on the object, as Terraform
// sees it: platform-reserved keys filtered out. The read-back and the fresh
// read an update makes before it writes (tftags.Prior.WithCurrent) both use
// it, so the two cannot disagree about what counts as a tag.
func (a *apiSecurityGroup) customerTags() map[string]string {
	return reservedmeta.FilterNetwork(a.Tags)
}
