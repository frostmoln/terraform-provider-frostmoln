package load_balancer

import (
	"context"
	"encoding/json"
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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/schemadoc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                   = &loadBalancerResource{}
	_ resource.ResourceWithImportState    = &loadBalancerResource{}
	_ resource.ResourceWithValidateConfig = &loadBalancerResource{}
	// Without this, a signature drift would silently drop the state upgrader,
	// and every customer still on schema version 0 would hit a hard "Unable to
	// Read Previously Saved State" error on their next plan.
	_ resource.ResourceWithUpgradeState = &loadBalancerResource{}
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

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before.
func (r *loadBalancerResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *loadBalancerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *loadBalancerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Version: 1,
		Description: "Manages a load balancer in the Frostmoln Cloud Platform. Load balancer creation and " +
			"deletion are asynchronous, so applies wait on the provisioning operation to complete.\n\n" +
			schemadoc.GatewayOrderingNote("frostmoln_load_balancer") + "\n\n" +
			scopedecl.Summary("frostmoln_load_balancer"),
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
				Description: "Reachability scheme: internal (default, private VIP only) or public (a bring-your-own public IP is attached to the VIP for external reachability). When public, public_ip_id is required. There is no in-place change between schemes; changing this forces a new resource.",
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
			"public_ip_id": schema.StringAttribute{
				Description: "ID of a pre-allocated, tenant-owned, unassociated public IP to attach to the VIP. " +
					"Required when scheme is public; must be omitted when scheme is internal. Changing this " +
					"forces a new resource.\n\n" +
					"Attaching it makes this load balancer depend on the VPC's gateway, which Terraform " +
					"cannot see — see the ordering note on this resource above.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"public_ip_address": schema.StringAttribute{
				Description: "The address of the attached public IP (present only when scheme is public).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"type": schema.StringAttribute{
				Description: "The load-balancer type: l7 (default) terminates HTTP/HTTPS and TLS and can insert headers; l4 serves TCP/UDP/SCTP only and preserves the client source IP. There is no in-place migration between types; changing this forces a new resource.",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("l7"),
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					// CANONICAL VALUES ONLY. Accepting the legacy spellings
					// here looks lenient and is in fact destructive: a config
					// value is never replaced by the schema default, and
					// nothing canonicalises the PLAN value, so `type =
					// "amphora"` would sit in the plan as "amphora" against a
					// state the upgrader wrote as "l7" -- unequal, on an
					// attribute that forces replacement. The practitioner's
					// most likely edit (rename the attribute, keep the value)
					// would then destroy and recreate every load balancer,
					// which is the exact outcome the state upgrader exists to
					// prevent.
					//
					// Rejecting them is strictly safer: removing the old
					// attribute already forces a config edit in the same
					// session (Terraform raises "Unsupported argument" during
					// config evaluation, before any plan), so there is no
					// window in which a legacy value is useful. A leftover
					// "amphora" now gets a validation error naming the allowed
					// values instead of a silent destroy plan.
					stringvalidator.OneOf("l7", "l4"),
				},
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID for the load balancer (type l7 only). Changing this forces a new resource.",
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
				Description: "The port ID backing the load balancer VIP.",
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
				Description: "The provisioning status of the load balancer.",
				Computed:    true,
			},
			"operating_status": schema.StringAttribute{
				Description: "The operating status of the load balancer.",
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
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (15m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

// ValidateConfig enforces the scheme<->public_ip_id invariant at plan time:
// a public load balancer must reference a public IP; an internal one must not.
// Unknown (interpolated) values are skipped — the backend stays authoritative.
func (r *loadBalancerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg LoadBalancerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Scheme.IsUnknown() || cfg.PublicIPID.IsUnknown() {
		return
	}

	// A null scheme defaults to internal (schema default applied after config).
	scheme := "internal"
	if !cfg.Scheme.IsNull() {
		scheme = cfg.Scheme.ValueString()
	}
	hasFIP := !cfg.PublicIPID.IsNull() && cfg.PublicIPID.ValueString() != ""

	switch scheme {
	case "public":
		if !hasFIP {
			resp.Diagnostics.AddAttributeError(
				path.Root("public_ip_id"),
				"Missing public_ip_id",
				`public_ip_id is required when scheme is "public".`,
			)
		}
	case "internal":
		if hasFIP {
			resp.Diagnostics.AddAttributeError(
				path.Root("public_ip_id"),
				"Unexpected public_ip_id",
				`public_ip_id must not be set when scheme is "internal" (the default); set scheme = "public" to attach a public IP.`,
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

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/load-balancers"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Load Balancer", err.Error())
		return
	}

	// the created-at floor for the discovery sweep
	applyStarted := time.Now().UTC()
	floor := applyStarted.Add(-time.Minute)
	subject := fmt.Sprintf("the load balancer %q", plan.Name.ValueString())

	var lbID string
	if apiResp.IsAccepted() {
		// Async create: 202 with an Operation body. Poll the operation, then
		// fetch the created load balancer by the operation's resourceId.
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", err.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Load Balancer", subject, waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			r.adoptCreatedObject(ctx, plan, floor, waitErr, resp)
			return
		}
		lbID = done.ResourceID
		if lbID == "" {
			// The operation COMPLETED without its resourceId (degraded
			// provisioning). The load balancer exists; the sweep resolves it
			// by name.
			r.adoptCreatedObject(ctx, plan, floor,
				fmt.Errorf("the create operation completed but returned no resource ID"), resp)
			return
		}
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

// adoptCreatedObject resolves an apply whose id the provider lost — a timed-out
// operation or a completed one that came back without a resourceId — by the
// family listing, and adopts the result honestly: found means a fresh read of
// what the platform HAS (not what the configuration asked for); absent means
// verified absence; unreadable names the platform's last word and the list
// path. Never `terraform state rm` (internal/orphan holds the copy).
func (r *loadBalancerResource) adoptCreatedObject(ctx context.Context, plan LoadBalancerModel, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	id := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Load Balancer",
		FMList:       "`fm lb list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath("/load-balancers"), nil)
			if err != nil {
				return "", err
			}
			var list apiLoadBalancerList
			if err := json.Unmarshal(apiResp.Body, &list); err != nil {
				return "", err
			}
			candidates := make([]orphan.Candidate, 0, len(list.Items))
			for _, it := range list.Items {
				candidates = append(candidates, orphan.Candidate{ID: it.ID, Name: it.Name, CreatedAt: it.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
	if id == "" {
		return
	}
	// HONEST READ: the platform's response, not the configuration's intent.
	lb, readErr := r.getLoadBalancer(ctx, id)
	if readErr != nil {
		resp.Diagnostics.AddError("Failed to Read Load Balancer After Adoption", readErr.Error())
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

	// Wait for the delete to complete on the timeouts block's delete budget.
	budgets := r.resolveBudgets(state.Timeouts)

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
		subject := state.ID.ValueString()

		if _, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); err != nil {
			orphan.AddDeleteOutcome(&resp.Diagnostics,
				r.client.ClassifyOperationFailure(ctx, op.OperationID),
				"Load Balancer", subject, err)
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
