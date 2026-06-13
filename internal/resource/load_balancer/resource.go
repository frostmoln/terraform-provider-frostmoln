package load_balancer

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                   = &loadBalancerResource{}
	_ resource.ResourceWithImportState    = &loadBalancerResource{}
	_ resource.ResourceWithValidateConfig = &loadBalancerResource{}
)

type loadBalancerResource struct {
	client       *client.Client
	pollInterval time.Duration // overridable for tests; defaults to 5s
	pollTimeout  time.Duration // overridable for tests; defaults to 15m (Amphora boot is slow)
}

// NewResource returns a new load balancer resource factory.
func NewResource() resource.Resource {
	return &loadBalancerResource{}
}

func (r *loadBalancerResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *loadBalancerResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

func (r *loadBalancerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *loadBalancerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a load balancer in the Frostmoln Cloud Platform. Load balancer creation and deletion are asynchronous (Octavia), so applies wait on the provisioning operation to complete.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the load balancer.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the load balancer.",
				Required:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID the load balancer belongs to. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID the load balancer VIP is placed in. Changing this forces a new resource.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A description of the load balancer.",
				Optional:    true,
			},
			"vip_address": schema.StringAttribute{
				Description: "The virtual IP address of the load balancer. If omitted, an address is allocated automatically. An explicitly-set VIP is effectively immutable: the backend does not support changing a VIP in place, so a changed vip_address in config is ignored on update. To move to a different VIP, taint the resource (terraform taint / -replace) to force a destroy and recreate.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"scheme": schema.StringAttribute{
				Description: "Reachability scheme: internal (default, private VIP only) or public (a bring-your-own floating IP is attached to the VIP for external reachability). When public, floating_ip_id is required. There is no in-place change between schemes; changing this forces a new resource.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("internal"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("internal", "public"),
				},
			},
			"floating_ip_id": schema.StringAttribute{
				Description: "ID of a pre-allocated, tenant-owned, unassociated floating IP to attach to the VIP. Required when scheme is public; must be omitted when scheme is internal. Changing this forces a new resource.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"floating_ip_address": schema.StringAttribute{
				Description: "The public IP address of the attached floating IP (present only when scheme is public).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"provider_type": schema.StringAttribute{
				Description: "The Octavia provider driver: amphora (default, full L7 + TLS) or ovn (L4-only, source-IP preserving, zero VM overhead). There is no in-place migration between providers; changing this forces a new resource. (Named provider_type because \"provider\" is a reserved Terraform attribute name.)",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("amphora"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("amphora", "ovn"),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The Octavia flavor ID for the load balancer (amphora provider only). Changing this forces a new resource.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the load balancer.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"vip_port_id": schema.StringAttribute{
				Description: "The Neutron port ID backing the load balancer VIP.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The overall status of the load balancer.",
				Computed:    true,
			},
			"provisioning_status": schema.StringAttribute{
				Description: "The Octavia provisioning status of the load balancer.",
				Computed:    true,
			},
			"operating_status": schema.StringAttribute{
				Description: "The Octavia operating status of the load balancer.",
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

// ValidateConfig enforces the scheme<->floating_ip_id invariant at plan time:
// a public load balancer must reference a floating IP; an internal one must not.
// Unknown (interpolated) values are skipped — the backend stays authoritative.
func (r *loadBalancerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg LoadBalancerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Scheme.IsUnknown() || cfg.FloatingIPID.IsUnknown() {
		return
	}

	// A null scheme defaults to internal (schema default applied after config).
	scheme := "internal"
	if !cfg.Scheme.IsNull() {
		scheme = cfg.Scheme.ValueString()
	}
	hasFIP := !cfg.FloatingIPID.IsNull() && cfg.FloatingIPID.ValueString() != ""

	switch scheme {
	case "public":
		if !hasFIP {
			resp.Diagnostics.AddAttributeError(
				path.Root("floating_ip_id"),
				"Missing floating_ip_id",
				`floating_ip_id is required when scheme is "public".`,
			)
		}
	case "internal":
		if hasFIP {
			resp.Diagnostics.AddAttributeError(
				path.Root("floating_ip_id"),
				"Unexpected floating_ip_id",
				`floating_ip_id must not be set when scheme is "internal" (the default); set scheme = "public" to attach a floating IP.`,
			)
		}
	}
}

func (r *loadBalancerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *loadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LoadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/load-balancers"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Load Balancer", err.Error())
		return
	}

	var lbID string
	if apiResp.IsAccepted() {
		// Async create: 202 with an Operation body. Poll the operation, then
		// fetch the created load balancer by the operation's resourceId.
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", err.Error())
			return
		}
		done, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout())
		if err != nil {
			resp.Diagnostics.AddError("Load Balancer Creation Failed", err.Error())
			return
		}
		lbID = done.ResourceID
	} else {
		// Synchronous create (201) returns the load balancer directly.
		lb, err := client.ParseResponse[apiLoadBalancer](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Load Balancer Response", err.Error())
			return
		}
		lbID = lb.ID
	}

	if lbID == "" {
		resp.Diagnostics.AddError(
			"Load Balancer Operation Returned No Resource ID",
			"The load balancer create operation completed but returned no resource ID. "+
				"The load balancer may exist in the backend without being tracked in Terraform state - check `fm lb list` and import it if necessary.",
		)
		return
	}

	lb, err := r.getLoadBalancer(ctx, lbID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Load Balancer After Creation", err.Error())
		return
	}

	plan.fromAPI(ctx, lb, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *loadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LoadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	lb, err := r.getLoadBalancer(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Load Balancer", err.Error())
		return
	}

	state.fromAPI(ctx, lb, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *loadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan LoadBalancerModel
	var state LoadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// LB-level update uses PUT (the network handler registers PUT /:lb_id).
	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s", id)), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Load Balancer", err.Error())
		return
	}

	lb, err := client.ParseResponse[apiLoadBalancer](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Load Balancer Response", err.Error())
		return
	}

	plan.fromAPI(ctx, lb, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *loadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LoadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	apiResp, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s", id)))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Load Balancer", err.Error())
		return
	}

	// Async delete: 202 with an Operation body. Poll until the operation
	// completes — the delete workflow verifies the resources are actually gone
	// before marking the operation completed.
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", err.Error())
			return
		}
		if _, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout()); err != nil {
			resp.Diagnostics.AddError("Load Balancer Deletion Failed", err.Error())
			return
		}
	}
}

func (r *loadBalancerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// getLoadBalancer fetches a load balancer by ID.
func (r *loadBalancerResource) getLoadBalancer(ctx context.Context, id string) (*apiLoadBalancer, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/load-balancers/%s", id)), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiLoadBalancer](apiResp)
}
