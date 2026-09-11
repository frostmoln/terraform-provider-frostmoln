package subnet

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/orphan"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &subnetResource{}
	_ resource.ResourceWithImportState = &subnetResource{}
)

type subnetResource struct {
	client *client.Client

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the accessors keep the values this
	// resource has always hardcoded (2s interval, 5m budget) when unset.
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewResource returns a new subnet resource.
func NewResource() resource.Resource {
	return &subnetResource{}
}

func (r *subnetResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 2 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — Create and Delete have always
// polled the provisioning operation against 5m.
func (r *subnetResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 5 * time.Minute
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before.
func (r *subnetResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *subnetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subnet"
}

func (r *subnetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a subnet in the Frostmoln Cloud Platform." + "\n\n" + scopedecl.Summary("frostmoln_subnet"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the subnet.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the subnet.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "A description of the subnet.",
				Optional:    true,
			},
			"cidr": schema.StringAttribute{
				Description: "The CIDR block for the subnet.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The ID of the VPC this subnet belongs to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"zone": schema.StringAttribute{
				Description: "The availability zone for the subnet.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"gateway_ip": schema.StringAttribute{
				Description: "The gateway IP address for the subnet.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"dns_servers": schema.ListAttribute{
				// Computed: the backend stamps the platform default DNS
				// servers when none are given, so an Optional-only attribute
				// would fail every unset-dns_servers apply with "provider
				// produced inconsistent result" (same server-defaulted shape
				// as gateway_ip above).
				Description: "The DNS server addresses for the subnet. Defaults to the platform DNS servers.",
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
					listplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the subnet.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the subnet.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// NO UseStateForUnknown, deliberately, unlike every attribute around
			// it. Those are fixed at create (id, cidr, zone, created_at) or
			// change only when Terraform changes them, so pinning the prior
			// value is right and saves a spurious "known after apply".
			//
			// This one is a LIVE GAUGE. It is the subnet's free-address count,
			// which every instance launch and every NIC delete moves, whether or
			// not Terraform is involved. UseStateForUnknown makes the plan ASSERT
			// the value the last read returned, so if anything consumes or frees
			// an address between plan and apply the provider returns a different
			// number and Terraform fails the apply with "Provider produced
			// inconsistent result after apply" -- on a change the customer made
			// to something else entirely, like a tag.
			//
			// That was unreachable only by accident: the network service's
			// CountBySubnet sent the Frostmoln tenant id as Neutron's tenant_id,
			// which matches nothing under project scope, so this number was a
			// CONSTANT (the whole pool) for every subnet of every tenant and
			// could not disagree with itself. Fixing that (network's CountBySubnet
			// scope fix) makes
			// it a real, moving figure, so the modifier has to go with it.
			// Computed with no modifier plans as "(known after apply)", which is
			// what a value the platform computes at read time actually is.
			"available_ips": schema.Int64Attribute{
				Description: "The number of available IP addresses in the subnet.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The creation timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: defaults keep the values this
			// resource has always hardcoded (5m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

func (r *subnetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *subnetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/subnets"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Subnet", err.Error())
		return
	}

	// Subnet create routes through provisioning → 202 + an Operation envelope
	// (operationId only, NOT the subnet). Poll the operation, then read by its
	// resolved resourceId. A non-202 body is parsed directly for a sync backend.
	applyStarted := time.Now().UTC() // the created-at floor for the discovery sweep
	floor := applyStarted.Add(-time.Minute)

	// The customer's timeouts block, with defaults identical to the values
	// this resource has always hardcoded.
	budgets := r.resolveBudgets(plan.Timeouts)

	var subnet apiSubnet
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "Subnet", fmt.Sprintf("the subnet %q", plan.Name.ValueString()), waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			r.adoptCreatedObject(ctx, plan, floor, waitErr, resp)
			return
		}
		if done.ResourceID == "" {
			// The operation COMPLETED without its resourceId (degraded
			// provisioning). The subnet exists; the sweep resolves it by name.
			r.adoptCreatedObject(ctx, plan, floor,
				fmt.Errorf("the subnet create operation completed but returned no resource ID"), resp)
			return
		}
		readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/subnets/%s", done.ResourceID)), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Subnet After Creation", readErr.Error())
			return
		}
		if err := json.Unmarshal(readResp.Body, &subnet); err != nil {
			resp.Diagnostics.AddError("Failed to Parse Subnet Response", err.Error())
			return
		}
	} else if err := json.Unmarshal(apiResp.Body, &subnet); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Subnet Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &subnet, &resp.Diagnostics)
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
func (r *subnetResource) adoptCreatedObject(ctx context.Context, plan SubnetModel, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	vpcID := plan.VPCID.ValueString()
	id := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "Subnet",
		FMList:       "`fm network subnet list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath("/subnets"), nil)
			if err != nil {
				return "", err
			}
			var list apiSubnetList
			if err := json.Unmarshal(apiResp.Body, &list); err != nil {
				return "", err
			}
			// The listing is tenant-wide, so narrow to the VPC this apply
			// targets before the pick — the name alone can match a subnet
			// of another VPC and adopt the wrong object.
			candidates := make([]orphan.Candidate, 0, len(list.Items))
			for _, it := range list.Items {
				if vpcID != "" && it.VPCID != vpcID {
					continue
				}
				candidates = append(candidates, orphan.Candidate{ID: it.ID, Name: it.Name, CreatedAt: it.CreatedAt})
			}
			return orphan.PickCreated(candidates, plan.Name.ValueString(), floor)
		},
	})
	if id == "" {
		return
	}
	// HONEST READ: the platform's response, not the configuration's intent.
	readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/subnets/%s", id)), nil)
	if readErr != nil {
		resp.Diagnostics.AddError("Failed to Read Subnet After Adoption", readErr.Error())
		return
	}
	var adopted apiSubnet
	if err := json.Unmarshal(readResp.Body, &adopted); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Subnet Response", err.Error())
		return
	}
	plan.fromAPI(ctx, &adopted, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subnetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/subnets/%s", state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Subnet", err.Error())
		return
	}

	var subnet apiSubnet
	if err := json.Unmarshal(apiResp.Body, &subnet); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Subnet Response", err.Error())
		return
	}

	state.fromAPI(ctx, &subnet, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *subnetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SubnetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state SubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// PUT, not PATCH — network registers PUT for the in-place update and
	// nothing registers PATCH. See the frostmoln_vpc Update for the full note.
	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/subnets/%s", state.ID.ValueString())), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Subnet", err.Error())
		return
	}

	var subnet apiSubnet
	if err := json.Unmarshal(apiResp.Body, &subnet); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Subnet Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &subnet, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subnetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SubnetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/subnets/%s", state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Subnet", err.Error())
		return
	}

	// Subnet delete routes through provisioning → 202 + an async workflow. Wait
	// for the operation to complete before returning: the DELETE alone returns
	// while the old subnet is still being torn down, so a REPLACE
	// (destroy-then-create) races — the network create is idempotent-by-
	// (name+cidr+vpc) and resolves to the still-deleting old subnet's id, so the
	// post-create GET 404s on that stale id (Ambix 019f2176 Bug 2). Waiting on
	// the operation also surfaces the real workflow error (e.g. subnet in use)
	// instead of a generic timeout. Mirrors subnet Create and volume Delete.
	if apiResp.IsAccepted() {
		op, err := client.ParseResponse[client.Operation](apiResp)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", err.Error())
			return
		}
		subject := state.ID.ValueString()

		// Delete waits on the timeouts block's delete budget, classified like
		// the rest of the surface: the row stays in both arms, and the copy
		// tells the practitioner which arm they are in.
		budgets := r.resolveBudgets(state.Timeouts)
		if _, err := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); err != nil {
			orphan.AddDeleteOutcome(&resp.Diagnostics,
				r.client.ClassifyOperationFailure(ctx, op.OperationID),
				"Subnet", subject, err)
		}
	}
}

func (r *subnetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
