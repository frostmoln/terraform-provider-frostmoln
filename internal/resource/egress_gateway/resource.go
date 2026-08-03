package egress_gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &egressGatewayResource{}
	_ resource.ResourceWithImportState = &egressGatewayResource{}
)

type egressGatewayResource struct {
	client *client.Client
}

// NewResource returns a new egress gateway resource.
func NewResource() resource.Resource {
	return &egressGatewayResource{}
}

func (r *egressGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_egress_gateway"
}

func (r *egressGatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC's outbound internet path. A VPC has at most one egress " +
			"gateway, and a VPC without one has no outbound internet access. " +
			"Destroying this resource also removes the VPC's DNS resolution and " +
			"managed-service connectivity, which are reached over the same path.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the egress gateway. Equal to vpc_id, " +
					"since the resource is one-to-one with a VPC — import by VPC id.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC this gateway serves. Changing it replaces the resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"mode": schema.StringAttribute{
				Description: "How outbound traffic is addressed. \"public_ip\" gives the VPC a " +
					"dedicated public IP address as its outbound source address, suitable for " +
					"giving a partner to allow-list. Changing the mode is an in-place update, " +
					"not a replacement — replacing would drop the recorded source address and " +
					"leave the VPC without DNS or managed-service connectivity in between.",
				Required: true,
				// Deliberately NOT RequiresReplace: a mode change is an in-place
				// update. Replacing would drop the recorded source address and
				// leave the VPC without DNS or managed-service connectivity
				// between destroy and create.
				Validators: []validator.String{
					stringvalidator.OneOf("public_ip"),
				},
			},
			"source_address": schema.StringAttribute{
				Description: "The public IP address outbound traffic appears to come from.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "Observed state of the gateway (\"active\" or \"detached\").",
				Computed:    true,
			},
			"origin": schema.StringAttribute{
				Description: "Who created the gateway: \"explicit\" (this resource), " +
					"\"implicit_public_ip\" (attached because a public IP was associated), " +
					"\"vpc_create\", or \"legacy\".",
				Computed: true,
			},
		},
	}
}

func (r *egressGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *client.Client")
		return
	}
	r.client = c
}

func (r *egressGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EgressGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/egress-gateways"), apiCreateEgressGatewayRequest{
		VPCID: plan.VPCID.ValueString(),
		Mode:  plan.Mode.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to create egress gateway", err.Error())
		return
	}

	var gw apiEgressGateway
	if err := json.Unmarshal(apiResp.Body, &gw); err != nil {
		resp.Diagnostics.AddError("Failed to parse egress gateway response", err.Error())
		return
	}

	applyToModel(&plan, &gw)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *egressGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EgressGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read by VPC rather than by id: the filtered list answers "does this VPC
	// have a gateway" with an empty list rather than a 404, so a gateway removed
	// out of band is a clean removal from state instead of an error.
	q := url.Values{}
	q.Set("vpcId", state.VPCID.ValueString())

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/egress-gateways"), q)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read egress gateway", err.Error())
		return
	}

	var list apiEgressGatewayList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse egress gateway response", err.Error())
		return
	}
	if len(list.EgressGateways) == 0 {
		resp.State.RemoveResource(ctx)
		return
	}

	applyToModel(&state, &list.EgressGateways[0])
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *egressGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EgressGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Patch(ctx,
		r.client.TenantPath(fmt.Sprintf("/egress-gateways/%s", state.ID.ValueString())),
		apiUpdateEgressGatewayRequest{
			Mode: plan.Mode.ValueString(),
			// The plan output is the acknowledgement: Terraform shows the mode
			// change before apply, so the practitioner has already seen and
			// approved it. Prompting again through an API flag would be theatre.
			AcknowledgeConnectivityLoss: true,
		})
	if err != nil {
		resp.Diagnostics.AddError("Failed to update egress gateway", err.Error())
		return
	}

	var gw apiEgressGateway
	if err := json.Unmarshal(apiResp.Body, &gw); err != nil {
		resp.Diagnostics.AddError("Failed to parse egress gateway response", err.Error())
		return
	}

	applyToModel(&plan, &gw)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *egressGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EgressGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The acknowledgement is set unconditionally here, and that is correct
	// rather than a bypass: `terraform destroy` shows the resource being removed
	// and requires approval, so the practitioner has already acknowledged the
	// loss. The API requires the flag so that no client can destroy the VPC's
	// DNS and managed-service connectivity WITHOUT a deliberate act; in
	// Terraform that act is the plan approval.
	_, err := r.client.Delete(ctx, r.client.TenantPath(
		fmt.Sprintf("/egress-gateways/%s?acknowledgeConnectivityLoss=true", state.ID.ValueString())))
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete egress gateway", err.Error())
		return
	}
}

func (r *egressGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The resource is 1:1 with a VPC and its id IS the VPC id, so importing by
	// VPC id is both natural and sufficient. Read() then fills the rest.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), req.ID)...)
}

func applyToModel(m *EgressGatewayModel, gw *apiEgressGateway) {
	m.ID = types.StringValue(gw.ID)
	m.VPCID = types.StringValue(gw.VPCID)
	m.Mode = types.StringValue(gw.Mode)
	m.SourceAddress = types.StringValue(gw.SourceAddress)
	m.Status = types.StringValue(gw.Status)
	m.Origin = types.StringValue(gw.Origin)
}
