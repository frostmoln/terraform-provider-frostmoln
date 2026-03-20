package security_group_rule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &securityGroupRuleResource{}
	_ resource.ResourceWithImportState = &securityGroupRuleResource{}
)

type securityGroupRuleResource struct {
	client *client.Client
}

// NewResource returns a new security group rule resource.
func NewResource() resource.Resource {
	return &securityGroupRuleResource{}
}

func (r *securityGroupRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group_rule"
}

func (r *securityGroupRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a security group rule in the Frostmoln Cloud Platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the rule.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"security_group_id": schema.StringAttribute{
				Description: "The ID of the security group this rule belongs to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"direction": schema.StringAttribute{
				Description: "The direction of the rule: ingress or egress.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"protocol": schema.StringAttribute{
				Description: "The protocol: tcp, udp, icmp, or any.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"port_range_min": schema.Int64Attribute{
				Description: "The minimum port number.",
				Optional:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"port_range_max": schema.Int64Attribute{
				Description: "The maximum port number.",
				Optional:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"remote_cidr": schema.StringAttribute{
				Description: "The remote CIDR block.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"remote_group_id": schema.StringAttribute{
				Description: "The remote security group ID.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A description of the rule.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *securityGroupRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData),
		)
		return
	}

	r.client = c
}

func (r *securityGroupRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecurityGroupRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sgID := plan.SecurityGroupID.ValueString()
	createReq := plan.toCreateRequest()

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s/rules", sgID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Security Group Rule", err.Error())
		return
	}

	var rule apiSecurityGroupRule
	if err := json.Unmarshal(apiResp.Body, &rule); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Security Group Rule Response", err.Error())
		return
	}

	plan.fromAPI(sgID, &rule)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securityGroupRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecurityGroupRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sgID := state.SecurityGroupID.ValueString()
	ruleID := state.ID.ValueString()

	// Get the parent security group to find the rule
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s", sgID)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Security Group", err.Error())
		return
	}

	var sg apiSecurityGroupWithRules
	if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Security Group Response", err.Error())
		return
	}

	// Find the rule in the security group
	var found *apiSecurityGroupRule
	for i := range sg.Rules {
		if sg.Rules[i].ID == ruleID {
			found = &sg.Rules[i]
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	state.fromAPI(sgID, found)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *securityGroupRuleResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All fields are ForceNew, so Update should never be called.
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Security group rules are immutable. All changes require replacement.",
	)
}

func (r *securityGroupRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecurityGroupRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sgID := state.SecurityGroupID.ValueString()
	ruleID := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s/rules/%s", sgID, ruleID)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Security Group Rule", err.Error())
		return
	}
}

func (r *securityGroupRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {security_group_id}/{rule_id}
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {security_group_id}/{rule_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("security_group_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
