package application_gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/schemadoc"
)

var (
	_ resource.Resource                   = &gatewayResource{}
	_ resource.ResourceWithImportState    = &gatewayResource{}
	_ resource.ResourceWithConfigure      = &gatewayResource{}
	_ resource.ResourceWithValidateConfig = &gatewayResource{}
)

// Provisioning waits. VARIABLES, not constants, so a test can shrink them:
// with the real values a test that drives a failing wait takes twenty minutes,
// which means in practice it is never written.
var (
	createTimeout = 20 * time.Minute
	deleteTimeout = 15 * time.Minute
	pollInterval  = 5 * time.Second
)

type gatewayResource struct {
	client *client.Client
}

// NewResource returns a new Application Gateway resource factory.
func NewResource() resource.Resource {
	return &gatewayResource{}
}

func (r *gatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_application_gateway"
}

func (r *gatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Frostmoln Application Gateway: one Public IP fronting many services " +
			"in your VPC, routed by host and URL path, with TLS termination and a Web Application Firewall.\n\n" +
			"Listeners, routes, backend pools, backends and published WAF versions are all " +
			"**authored** against this gateway and do not take effect until they are applied to the " +
			"appliance. `config_generation` is what has been authored; `config_revision` is what the " +
			"appliance has acknowledged. When they differ there is a change the gateway is not serving." +
			"\n\n" +
			schemadoc.GatewayOrderingNote("frostmoln_application_gateway"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Application Gateway.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the Application Gateway.",
				Required:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor (size) of the Application Gateway, e.g. `agw.gp1.small`. " +
					"Changing this forces a new resource. Available sizes and their structural limits " +
					"are in the `frostmoln_appgw_flavors` data source.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"version": schema.StringAttribute{
				Description: "The data-plane engine version. Omit to use the catalog default. " +
					"Changing this forces a new resource.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC the gateway lives in. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet the gateway's appliance attaches to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"public_ip_mode": schema.StringAttribute{
				Description: "Where the gateway's public address comes from.\n\n" +
					"* `allocated` (default) — the platform draws an address from the pool and " +
					"**releases it when the gateway is destroyed**.\n" +
					"* `selected` — use an address you already hold, named by `public_ip_id`. It is " +
					"used as-is and is **never released** when the gateway is destroyed.\n\n" +
					"Changing this forces a new resource.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("allocated"),
				Validators: []validator.String{
					stringvalidator.OneOf("allocated", "selected"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"public_ip_id": schema.StringAttribute{
				Description: "The ID of a Public IP you already own, used when `public_ip_mode` is " +
					"`selected`. Changing this forces a new resource.\n\n" +
					"This is **not** populated for a pool-allocated gateway: the allocated address is " +
					"visible as `public_ip` and has no id here, so that this attribute round-trips " +
					"exactly what you configured.\n\n" +
					"See the gateway ordering note on this resource: an Application Gateway attaches " +
					"a public address in **both** `public_ip_mode` values, so that note applies " +
					"whether or not you set this attribute.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The gateway's lifecycle state.",
				Computed:    true,
			},
			"public_ip": schema.StringAttribute{
				Description: "The gateway's public address.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The appliance's address inside your VPC.",
				Computed:    true,
			},
			"vpc_cidr": schema.StringAttribute{
				Description: "The gateway's VPC range. Backend addresses are validated against it.",
				Computed:    true,
			},
			"config_generation": schema.Int64Attribute{
				Description: "The configuration generation that has been AUTHORED.",
				Computed:    true,
			},
			"config_revision": schema.Int64Attribute{
				Description: "The configuration generation the appliance has ACKNOWLEDGED. Null until " +
					"something has been applied. When it differs from `config_generation` there is a " +
					"change the gateway is not yet serving.",
				Computed: true,
			},
			"config_status": schema.StringAttribute{
				Description: "What happened to the last configuration apply: `pending`, `applying`, " +
					"`applied`, `failed` or `unknown`. Distinct from `status`, which is the appliance's " +
					"lifecycle — a gateway can be `running` while its last configuration change was refused.",
				Computed: true,
			},
			"config_detail": schema.StringAttribute{
				Description: "The appliance's own words when it refused a configuration.",
				Computed:    true,
			},
			"waf_policy_id": schema.StringAttribute{
				Description: "The WAF policy attached to this gateway, if any.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The creation timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

// ValidateConfig refuses the two public-IP combinations that are wrong before
// they reach the server.
//
// Worth catching at plan time rather than apply time specifically because
// getting this pair wrong is how a practitioner either loses an address they
// own or is billed for one they did not ask for.
func (r *gatewayResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg GatewayModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// An unknown value cannot be judged yet -- it is resolved at apply.
	if cfg.PublicIPMode.IsUnknown() || cfg.PublicIPID.IsUnknown() {
		return
	}
	mode := cfg.PublicIPMode.ValueString()
	if cfg.PublicIPMode.IsNull() {
		mode = "allocated"
	}
	hasID := !cfg.PublicIPID.IsNull() && cfg.PublicIPID.ValueString() != ""

	switch {
	case mode == "allocated" && hasID:
		resp.Diagnostics.AddAttributeError(path.Root("public_ip_id"),
			"public_ip_id Requires public_ip_mode = \"selected\"",
			"With public_ip_mode = \"allocated\" the platform draws the address itself and "+
				"releases it when the gateway is destroyed, so naming one has no effect. "+
				"Set public_ip_mode = \"selected\" to use an address you already own.")
	case mode == "selected" && !hasID:
		resp.Diagnostics.AddAttributeError(path.Root("public_ip_id"),
			"public_ip_id Is Required With public_ip_mode = \"selected\"",
			"public_ip_mode = \"selected\" means \"use an address I already hold\", so "+
				"public_ip_id must name it.")
	}
}

func (r *gatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *gatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan GatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	mode := plan.PublicIPMode.ValueString()
	if mode == "" {
		mode = "allocated"
	}
	createReq := apiCreateGatewayRequest{
		Name:         plan.Name.ValueString(),
		FlavorID:     plan.FlavorID.ValueString(),
		VPCID:        plan.VPCID.ValueString(),
		SubnetID:     plan.SubnetID.ValueString(),
		PublicIPMode: mode,
	}
	if !plan.Version.IsNull() && !plan.Version.IsUnknown() {
		createReq.Version = plan.Version.ValueString()
	}
	if !plan.PublicIPID.IsNull() && !plan.PublicIPID.IsUnknown() {
		createReq.PublicIPID = plan.PublicIPID.ValueString()
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/application-gateways"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Application Gateway", err.Error())
		return
	}

	created, err := client.ParseResponse[apiCreateGatewayResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Application Gateway Response", err.Error())
		return
	}
	if created.Gateway == nil || created.Gateway.ID == "" {
		resp.Diagnostics.AddError("Application Gateway Create Returned No Gateway",
			"The server accepted the create but returned no gateway to track.")
		return
	}

	// 🔴 WRITE STATE BEFORE WAITING. The gateway row EXISTS from this point on,
	// and the wait can fail or the run be interrupted. Without this the
	// practitioner is left with a real gateway -- billed, holding a Public IP --
	// that Terraform has never heard of and will never destroy.
	plan.fromAPI(created.Gateway)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if created.OperationID != "" {
		if _, waitErr := r.client.WaitForOperation(ctx, created.OperationID, pollInterval, createTimeout); waitErr != nil {
			resp.Diagnostics.AddError("Application Gateway Provisioning Failed",
				fmt.Sprintf("The gateway %s was created but provisioning did not complete: %s\n\n"+
					"It is recorded in state; inspect it with `terraform state show` and destroy or "+
					"retry as appropriate.", created.Gateway.ID, waitErr.Error()))
			return
		}
	}

	// Re-read: the address, the private IP and the VPC range only exist once
	// the saga has run, so the accepted body cannot carry them.
	gw, err := r.read(ctx, created.Gateway.ID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Application Gateway After Creation", err.Error())
		return
	}
	plan.fromAPI(gw)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state GatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	gw, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Application Gateway", err.Error())
		return
	}
	// A soft-deleted gateway reads back with status "deleted". Treating that as
	// present would leave a tombstone in state that never converges.
	if gw.Status == "deleted" {
		resp.State.RemoveResource(ctx)
		return
	}
	state.fromAPI(gw)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state GatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Name is the only in-place change; every other attribute carries
	// RequiresReplace, so reaching here with anything else changed is not
	// possible.
	name := plan.Name.ValueString()
	apiResp, err := r.client.Patch(ctx,
		r.client.TenantPath(fmt.Sprintf("/application-gateways/%s", state.ID.ValueString())),
		apiUpdateGatewayRequest{Name: &name})
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Application Gateway", err.Error())
		return
	}
	gw, err := client.ParseResponse[apiGateway](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Application Gateway Response", err.Error())
		return
	}
	plan.fromAPI(gw)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state GatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Delete(ctx,
		r.client.TenantPath(fmt.Sprintf("/application-gateways/%s", state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Application Gateway", err.Error())
		return
	}

	// A 202 carries an operation to wait on. Anything else means the delete was
	// synchronous and there is nothing to poll.
	if !apiResp.IsAccepted() {
		return
	}
	op, err := client.ParseResponse[apiOperationResponse](apiResp)
	if err != nil || op.OperationID == "" {
		// The delete saga IS running; this client simply cannot watch it.
		// Returning silently would let Terraform drop the resource and report
		// success — and if the saga then fails, the appliance, its volume, both
		// security groups and (in allocated mode) the Public IP stay in the
		// tenant's project, billing, with nothing in state that will ever
		// destroy them. That is the failure the create path's
		// write-state-before-waiting exists to prevent, arriving from the other
		// end.
		resp.Diagnostics.AddWarning("The Delete Was Accepted But Could Not Be Tracked",
			"The server accepted the delete and returned no operation this provider could read, "+
				"so it has not been watched to completion. The gateway has been removed from "+
				"state. Confirm it is actually gone — and that its Public IP was released or "+
				"kept as you intended — before assuming the destroy finished.")
		return
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, pollInterval, deleteTimeout); waitErr != nil {
		resp.Diagnostics.AddError("Application Gateway Deletion Failed", waitErr.Error())
	}
}

// ImportState validates the id rather than passing it through.
//
// 🔴 THIS IS THE ID THAT BECOMES THE FINAL SEGMENT OF A DELETE. The client
// assembles URLs with path.Join, which cleans dot segments, so a passthrough id
// of "../../../v1/tenants/<t>/vpcs/<v>" would address a different resource
// entirely — with the caller's own credential — and `terraform destroy` would
// then issue the DELETE against it. Terraform 1.5+ `import` blocks take the id
// as a config expression, so it can come from a tfvars file or a shared module.
//
// Every other resource in this family already parses its composite id through
// ParseImportID; a single-segment id is not a reason to skip the check.
func (r *gatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0])...)
}

func (r *gatewayResource) read(ctx context.Context, id string) (*apiGateway, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/application-gateways/%s", id)), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiGateway](apiResp)
}
