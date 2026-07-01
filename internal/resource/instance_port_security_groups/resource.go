// Package instance_port_security_groups implements the
// frostmoln_instance_port_security_groups Terraform resource.
package instance_port_security_groups

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

const (
	defaultPollInterval = 5 * time.Second
	defaultPollTimeout  = 10 * time.Minute
)

var (
	_ resource.Resource                = &instancePortSecurityGroupsResource{}
	_ resource.ResourceWithImportState = &instancePortSecurityGroupsResource{}
)

// NewResource returns a new instance-port security-groups resource factory.
func NewResource() resource.Resource {
	return &instancePortSecurityGroupsResource{}
}

type instancePortSecurityGroupsResource struct {
	client *client.Client

	// Overridable for tests; default to defaultPollInterval / defaultPollTimeout.
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func (r *instancePortSecurityGroupsResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return defaultPollInterval
}

func (r *instancePortSecurityGroupsResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return defaultPollTimeout
}

func (r *instancePortSecurityGroupsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance_port_security_groups"
}

func (r *instancePortSecurityGroupsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the security groups on a SINGLE network port of a multi-NIC compute instance, " +
			"leaving the instance's other ports untouched. Use this when an instance's ports need DIFFERENT " +
			"security-group sets; for one set applied uniformly across every port, set security_groups on the " +
			"frostmoln_instance resource instead (the two are mutually exclusive ways to manage the same ports).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Composite identifier ({instance_id}/{port_id}).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the instance the port belongs to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"port_id": schema.StringAttribute{
				Description: "The Neutron port ID to set security groups on. Port IDs are shown in the " +
					"instance's per-port security-group breakdown (GET .../security-groups).",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"security_groups": schema.SetAttribute{
				Description: "The security-group IDs (Neutron UUIDs) to set on the port (replace semantics — any " +
					"security group not listed is removed from the port). An empty set clears all security groups on " +
					"the port, leaving it on the VPC default-drop (typically no inbound access).",
				Required:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *instancePortSecurityGroupsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *instancePortSecurityGroupsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan InstancePortSecurityGroupsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sgIDs := r.sgIDsFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.setPortSecurityGroups(ctx, plan.InstanceID.ValueString(), plan.PortID.ValueString(), sgIDs); err != nil {
		resp.Diagnostics.AddError("Failed to set port security groups", err.Error())
		return
	}

	if found := r.refresh(ctx, &plan, &resp.Diagnostics); resp.Diagnostics.HasError() {
		return
	} else if !found {
		resp.Diagnostics.AddError(
			"Port not found after update",
			fmt.Sprintf("Set security groups on port %s of instance %s, but the port was not present in the instance's port breakdown on read-back.",
				plan.PortID.ValueString(), plan.InstanceID.ValueString()),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instancePortSecurityGroupsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state InstancePortSecurityGroupsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if found := r.refresh(ctx, &state, &resp.Diagnostics); resp.Diagnostics.HasError() {
		return
	} else if !found {
		// The port (or its instance) is gone — the resource no longer exists.
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *instancePortSecurityGroupsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan InstancePortSecurityGroupsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sgIDs := r.sgIDsFromModel(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.setPortSecurityGroups(ctx, plan.InstanceID.ValueString(), plan.PortID.ValueString(), sgIDs); err != nil {
		resp.Diagnostics.AddError("Failed to set port security groups", err.Error())
		return
	}

	if found := r.refresh(ctx, &plan, &resp.Diagnostics); resp.Diagnostics.HasError() {
		return
	} else if !found {
		resp.Diagnostics.AddError(
			"Port not found after update",
			fmt.Sprintf("Set security groups on port %s of instance %s, but the port was not present on read-back.",
				plan.PortID.ValueString(), plan.InstanceID.ValueString()),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete is intentionally a no-op beyond removing the resource from state.
// Destroying just this resource stops Terraform managing the port's security
// groups; it does NOT strip them. Silently clearing a port's SGs on destroy
// would flip its firewall to default-drop — a surprising, security-relevant side
// effect. The port keeps whatever set it currently has; manage it again by
// re-adding the resource, or set it explicitly elsewhere.
func (r *instancePortSecurityGroupsResource) Delete(ctx context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	tflog.Info(ctx, "frostmoln_instance_port_security_groups delete is a no-op: the port's security groups are left unchanged")
}

func (r *instancePortSecurityGroupsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("Expected import ID in the form {instance_id}/{port_id}, got %q.", req.ID),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("port_id"), parts[1])...)
	// security_groups + id are populated by the subsequent Read.
}

// refresh reads the port's authoritative SG set from
// GET /instances/{id}/security-groups and updates the model (SecurityGroups +
// ID). It returns false when the instance or the port is gone (the caller drops
// the resource from state); a genuine read/parse error is reported via diags.
func (r *instancePortSecurityGroupsResource) refresh(ctx context.Context, m *InstancePortSecurityGroupsModel, diags *diag.Diagnostics) bool {
	sg, err := r.getSecurityGroups(ctx, m.InstanceID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			return false
		}
		diags.AddError("Failed to read instance security groups", err.Error())
		return false
	}

	port := sg.findPort(m.PortID.ValueString())
	if port == nil {
		return false
	}

	setVal, d := types.SetValueFrom(ctx, types.StringType, port.SecurityGroupIDs)
	diags.Append(d...)
	if diags.HasError() {
		return false
	}
	m.SecurityGroups = setVal
	m.ID = types.StringValue(m.InstanceID.ValueString() + "/" + m.PortID.ValueString())
	return true
}

// sgIDsFromModel extracts the configured security-group IDs as a []string.
func (r *instancePortSecurityGroupsResource) sgIDsFromModel(ctx context.Context, m *InstancePortSecurityGroupsModel, diags *diag.Diagnostics) []string {
	var sgIDs []string
	diags.Append(m.SecurityGroups.ElementsAs(ctx, &sgIDs, false)...)
	return sgIDs
}

// getSecurityGroups fetches the authoritative per-port SG breakdown from
// GET /instances/{id}/security-groups (Neutron SG UUIDs).
func (r *instancePortSecurityGroupsResource) getSecurityGroups(ctx context.Context, instanceID string) (*apiInstanceSecurityGroups, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+instanceID+"/security-groups"), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiInstanceSecurityGroups](apiResp)
}

// setPortSecurityGroups REPLACES a single port's security groups via
// PUT /instances/{id}/ports/{portId}/security-groups (replace semantics). An
// empty sgIDs clears all SGs on the port (clear flag set so the backend does not
// reject it as a probable dropped field). The PUT routes through provisioning
// and returns 202 + an Operation; we wait for it so the applied set is visible
// to the subsequent read-back (the change lands asynchronously).
func (r *instancePortSecurityGroupsResource) setPortSecurityGroups(ctx context.Context, instanceID, portID string, sgIDs []string) error {
	body := apiSetInstancePortSecurityGroupsRequest{
		SecurityGroupIDs:    sgIDs,
		ClearSecurityGroups: len(sgIDs) == 0,
	}
	apiResp, err := r.client.Put(ctx, r.client.TenantPath("/instances/"+instanceID+"/ports/"+portID+"/security-groups"), body)
	if err != nil {
		return err
	}
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			return fmt.Errorf("parse port security-group operation response: %w", opErr)
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout()); waitErr != nil {
			return fmt.Errorf("port security-group update did not complete: %w", waitErr)
		}
	}
	return nil
}
