// Package instance implements the fm_instance Terraform resource.
package instance

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
)

// InstanceModel is the Terraform state model for a compute instance.
type InstanceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	FlavorID        types.String `tfsdk:"flavor_id"`
	ImageID         types.String `tfsdk:"image_id"`
	Zone            types.String `tfsdk:"zone"`
	VPCID           types.String `tfsdk:"vpc_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	SecurityGroups  types.Set    `tfsdk:"security_groups"`
	SSHKeyNames     types.Set    `tfsdk:"ssh_key_names"`
	UserData        types.String `tfsdk:"user_data"`
	ConsolePassword types.String `tfsdk:"console_password"`
	UserDataHash    types.String `tfsdk:"user_data_hash"`
	Tags            types.Map    `tfsdk:"tags"`
	Status          types.String `tfsdk:"status"`
	FlavorName      types.String `tfsdk:"flavor_name"`
	ImageName       types.String `tfsdk:"image_name"`
	PrivateIP       types.String `tfsdk:"private_ip"`
	PublicIP        types.String `tfsdk:"public_ip"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// apiNestedRef is a nested object that only carries a name (flavor{}/image{}).
type apiNestedRef struct {
	Name string `json:"name"`
}

// apiInstanceNetwork is one element of the instance's networks[] array. The
// backend returns the VPC (network) and subnet a port is attached to here
// rather than as top-level scalars.
type apiInstanceNetwork struct {
	NetworkID string `json:"networkId"`
	SubnetID  string `json:"subnetId,omitempty"`
}

// apiInstance is the API representation of a compute instance. Field names match
// what the compute service actually serializes (see compute/internal/domain/instance.go):
// flavor/image are nested objects, IPs are arrays, user tags live under metadata,
// VPC/subnet only appear inside networks[], and there is no top-level region.
type apiInstance struct {
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Status     string               `json:"status"`
	FlavorID   string               `json:"flavorId"`
	Flavor     *apiNestedRef        `json:"flavor,omitempty"`
	ImageID    string               `json:"imageId"`
	Image      *apiNestedRef        `json:"image,omitempty"`
	Zone       string               `json:"availabilityZone,omitempty"`
	Networks   []apiInstanceNetwork `json:"networks,omitempty"`
	PrivateIPs []string             `json:"privateIps,omitempty"`
	PublicIPs  []string             `json:"publicIps,omitempty"`
	// Returned as the OpenStack-internal SG NAME (sg-<tenant>-<vpc>-<name>), not
	// the customer SG UUID. Intentionally NOT mapped to state in fromAPI — doing
	// so triggers "inconsistent result after apply" on this RequiresReplace attr.
	SecurityGroups []string          `json:"securityGroups,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      string            `json:"createdAt"`
}

// apiCreateInstanceRequest is the API request to create an instance. The create
// routes through provisioning, which expects sshKeyIds/securityGroupIds (see
// provisioning/internal/handler/http/instance_handler.go). User tags are sent as
// the `tags` map (provisioning maps them to instance metadata).
type apiCreateInstanceRequest struct {
	Name             string            `json:"name"`
	FlavorID         string            `json:"flavorId"`
	ImageID          string            `json:"imageId"`
	Zone             string            `json:"availabilityZone,omitempty"`
	VPCID            string            `json:"vpcId,omitempty"`
	SubnetID         string            `json:"subnetId,omitempty"`
	SecurityGroupIDs []string          `json:"securityGroupIds,omitempty"`
	SSHKeyIDs        []string          `json:"sshKeyIds,omitempty"`
	UserData         string            `json:"userData,omitempty"`
	ConsolePassword  string            `json:"consolePassword,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
}

// apiUpdateInstanceRequest is the API request to update an instance. Update
// routes to the compute service, which reads user tags under "metadata" (not the
// OpenStack []string "tags"); sending the map as "metadata" makes tags persist.
// The backend has no security-group update field — changing SGs post-create is
// not supported by the API, so it is omitted here.
type apiUpdateInstanceRequest struct {
	Name *string           `json:"name,omitempty"`
	Tags map[string]string `json:"metadata,omitempty"`
}

// apiResizeInstanceRequest is the body for POST /instances/{id}/resize.
type apiResizeInstanceRequest struct {
	FlavorID string `json:"flavorId"`
}

// computeUserDataHash returns the SHA256 hash of user data.
func computeUserDataHash(userData string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(userData)))
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *InstanceModel) toCreateRequest(ctx context.Context, diags *diag.Diagnostics) apiCreateInstanceRequest {
	req := apiCreateInstanceRequest{
		Name:     m.Name.ValueString(),
		FlavorID: m.FlavorID.ValueString(),
		ImageID:  m.ImageID.ValueString(),
	}

	if !m.Zone.IsNull() && !m.Zone.IsUnknown() {
		req.Zone = m.Zone.ValueString()
	}
	if !m.VPCID.IsNull() && !m.VPCID.IsUnknown() {
		req.VPCID = m.VPCID.ValueString()
	}
	if !m.SubnetID.IsNull() && !m.SubnetID.IsUnknown() {
		req.SubnetID = m.SubnetID.ValueString()
	}

	if !m.SecurityGroups.IsNull() && !m.SecurityGroups.IsUnknown() {
		var sgs []string
		diags.Append(m.SecurityGroups.ElementsAs(ctx, &sgs, false)...)
		req.SecurityGroupIDs = sgs
	}

	if !m.SSHKeyNames.IsNull() && !m.SSHKeyNames.IsUnknown() {
		var keys []string
		diags.Append(m.SSHKeyNames.ElementsAs(ctx, &keys, false)...)
		req.SSHKeyIDs = keys
	}

	if !m.UserData.IsNull() && !m.UserData.IsUnknown() {
		req.UserData = m.UserData.ValueString()
	}

	if !m.ConsolePassword.IsNull() && !m.ConsolePassword.IsUnknown() {
		req.ConsolePassword = m.ConsolePassword.ValueString()
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *InstanceModel) toUpdateRequest(ctx context.Context, diags *diag.Diagnostics) apiUpdateInstanceRequest {
	req := apiUpdateInstanceRequest{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		name := m.Name.ValueString()
		req.Name = &name
	}

	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags.Append(m.Tags.ElementsAs(ctx, &tags, false)...)
		req.Tags = tags
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
// It preserves the user_data and console_password fields from state since the API
// does not return them.
func (m *InstanceModel) fromAPI(ctx context.Context, inst *apiInstance, diags *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Status = types.StringValue(inst.Status)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.ImageID = types.StringValue(inst.ImageID)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	// flavor_name / image_name are derived from the nested flavor{}/image{}
	// objects (the backend has no top-level flavorName/imageName).
	if inst.Flavor != nil && inst.Flavor.Name != "" {
		m.FlavorName = types.StringValue(inst.Flavor.Name)
	} else {
		m.FlavorName = types.StringNull()
	}
	if inst.Image != nil && inst.Image.Name != "" {
		m.ImageName = types.StringValue(inst.Image.Name)
	} else {
		m.ImageName = types.StringNull()
	}

	if inst.Zone != "" {
		m.Zone = types.StringValue(inst.Zone)
	} else {
		m.Zone = types.StringNull()
	}

	// private_ip / public_ip are derived from the first element of the
	// privateIps[]/publicIps[] arrays (mirrors the portal normalizer).
	if len(inst.PrivateIPs) > 0 && inst.PrivateIPs[0] != "" {
		m.PrivateIP = types.StringValue(inst.PrivateIPs[0])
	} else {
		m.PrivateIP = types.StringNull()
	}
	if len(inst.PublicIPs) > 0 && inst.PublicIPs[0] != "" {
		m.PublicIP = types.StringValue(inst.PublicIPs[0])
	} else {
		m.PublicIP = types.StringNull()
	}

	// vpc_id / subnet_id are RequiresReplace create-time attributes that the
	// backend does not echo as top-level scalars (they live inside networks[]).
	// They are intentionally left untouched here so the value set by the user
	// at create is preserved on every refresh; overwriting from the response
	// would either null out the user's value or trigger a spurious replace.

	// security_groups is RequiresReplace and create-time. The backend read
	// returns the OpenStack-internal SG NAME (e.g. "sg-<tenant>-<vpc>-<name>",
	// compute mapper.go), not the customer SG UUID the user supplied at create.
	// It is therefore left untouched and preserved from plan/state — overwriting
	// from the response would trigger a spurious replace / inconsistent-result.

	// ssh_key_names is RequiresReplace and write-only on the wire (the backend
	// returns a single keyName, not the set the user supplied), so it is left
	// untouched and preserved from plan/state.

	// Tags come from the user metadata map (the backend has no top-level `tags`).
	// Platform-internal metadata (the frostmoln_ namespace) is injected by the backend
	// and is NOT a customer tag — filter it out, otherwise a null/unset tags plan is
	// overwritten by system keys on read-back ("inconsistent result after apply").
	userTags := reservedmeta.FilterInstance(inst.Metadata)
	if len(userTags) > 0 {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, userTags)
		diags.Append(d...)
		m.Tags = tagMap
	} else if !m.Tags.IsNull() {
		tagMap, d := types.MapValueFrom(ctx, types.StringType, map[string]string{})
		diags.Append(d...)
		m.Tags = tagMap
	} else {
		m.Tags = types.MapNull(types.StringType)
	}

	// user_data, user_data_hash and console_password are NOT set here because the API
	// doesn't return them. They are preserved from the existing state in the Read method.
}
