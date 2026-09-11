package vpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
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
	_ resource.Resource                = &vpcResource{}
	_ resource.ResourceWithImportState = &vpcResource{}
)

type vpcResource struct {
	client *client.Client

	// pollInterval and pollTimeout bound the waits for the provisioning
	// operations a write starts. Fields rather than constants so a test can
	// drive the timeout in milliseconds; the timeouts block sits on top of
	// these defaults (see resolveBudgets).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewResource returns a new VPC resource.
func NewResource() resource.Resource {
	return &vpcResource{}
}

func (r *vpcResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 2 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb. Create has always polled the provisioning operation against 5m;
// the delete wait (an async destroy that used to be reported as done the
// moment its 202 landed) defaults to the same value so the family keeps one
// number.
func (r *vpcResource) getPollTimeout() time.Duration {
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
func (r *vpcResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (r *vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC in the Frostmoln Cloud Platform.\n\n" +
			"**A VPC created through Terraform is an ISOLATED network until a `frostmoln_gateway` " +
			"is declared for it.** This resource deliberately carries no connectivity choice: in " +
			"Terraform the choice IS the presence, absence and `mode` of that separate resource, " +
			"so a VPC with no `frostmoln_gateway` has no outbound internet path — and no inbound " +
			"one either.\n\n" +
			"Platform DNS resolution and managed-service control-plane connectivity are reached over " +
			"routes that exist only while the gateway does, so they are absent too. Instances in a " +
			"gateway-less VPC cannot resolve names and cannot fetch anything from the internet — " +
			"which is what makes a `user_data` cloud-init step that installs packages or calls an " +
			"external endpoint fail on first boot. A managed database, cache or message broker that " +
			"is deployed INTO this VPC still answers on its private address: that traffic stays " +
			"inside the VPC and never crosses the gateway, though reaching it by name does need DNS. " +
			"This is not a fault to diagnose; it is what a VPC is before its outbound path is " +
			"declared.\n\n" +
			"Declare a `frostmoln_gateway` with `vpc_id` set to this VPC to give it that path — see " +
			"the example below and the `frostmoln_gateway` resource. Connectivity is a stated " +
			"choice, never one a VPC acquires because a field was omitted.\n\n" +
			"One thing to know before you conclude the gateway is missing: associating a public IP " +
			"with an instance in the VPC makes the platform attach a gateway implicitly, because a " +
			"public IP cannot work without one. Egress then starts working for the WHOLE VPC, not " +
			"just that instance, and the gateway reports `origin` = \"implicit_public_ip\". It is a " +
			"real gateway that Terraform did not declare — so prefer declaring `frostmoln_gateway` " +
			"explicitly, and see that resource for how an implicit gateway and an explicit one " +
			"interact." +
			"\n\n" + scopedecl.Summary("frostmoln_vpc"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the VPC.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the VPC.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "A description of the VPC.",
				Optional:    true,
			},
			"cidr": schema.StringAttribute{
				Description: "The CIDR block for the VPC.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the VPC.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the VPC.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Description: "Whether this is the default VPC.",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
			},
			"subnet_count": schema.Int64Attribute{
				Description: "The number of subnets in the VPC.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
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
			// resource has always hardcoded (5m per verb). A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
		},
	}
}

func (r *vpcResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VPCModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/vpcs"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create VPC", err.Error())
		return
	}

	// VPC create routes through provisioning → 202 + an Operation envelope
	// (operationId only, NOT the VPC). Poll the operation, then read by its
	// resolved resourceId. A non-202 body is parsed directly for a sync backend.
	// (Previously json.Unmarshal'd the envelope into apiVPC → empty ID → polled
	// /vpcs/{empty} against the real backend.)
	applyStarted := time.Now().UTC() // the created-at floor for the discovery sweep
	floor := applyStarted.Add(-time.Minute)
	budgets := r.resolveBudgets(plan.Timeouts)
	var vpc apiVPC
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Create)
		if waitErr != nil {
			if r.client.ClassifyOperationFailure(ctx, op.OperationID) == client.OperationRefused {
				orphan.AddCreateRefused(&resp.Diagnostics, "VPC", fmt.Sprintf("the VPC %q", plan.Name.ValueString()), waitErr)
				return
			}
			// UNKNOWN: the saga may still land. The sweep decides honestly.
			r.adoptCreatedObject(ctx, plan, floor, waitErr, resp)
			return
		}
		if done.ResourceID == "" {
			// The operation COMPLETED without its resourceId (degraded
			// provisioning). The VPC exists; the sweep resolves it by name.
			r.adoptCreatedObject(ctx, plan, floor,
				fmt.Errorf("the create operation completed but returned no resource ID"), resp)
			return
		}
		readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", done.ResourceID)), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read VPC After Creation", readErr.Error())
			return
		}
		if err := json.Unmarshal(readResp.Body, &vpc); err != nil {
			resp.Diagnostics.AddError("Failed to Parse VPC Response", err.Error())
			return
		}
	} else if err := json.Unmarshal(apiResp.Body, &vpc); err != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &vpc, &resp.Diagnostics)
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
func (r *vpcResource) adoptCreatedObject(ctx context.Context, plan VPCModel, floor time.Time, waitErr error, resp *resource.CreateResponse) {
	id := orphan.AdoptCreateOnTimeout(ctx, &resp.Diagnostics, orphan.CreateParams{
		ResourceName: "VPC",
		FMList:       "`fm network vpc list`",
		WaitErr:      waitErr,
		Resolve: func(ctx context.Context) (string, error) {
			apiResp, err := r.client.Get(ctx, r.client.TenantPath("/vpcs"), nil)
			if err != nil {
				return "", err
			}
			var list apiVPCList
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
	readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", id)), nil)
	if readErr != nil {
		resp.Diagnostics.AddError("Failed to Read VPC After Adoption", readErr.Error())
		return
	}
	var adopted apiVPC
	if err := json.Unmarshal(readResp.Body, &adopted); err != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Response", err.Error())
		return
	}
	plan.fromAPI(ctx, &adopted, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VPCModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read VPC", err.Error())
		return
	}

	var vpc apiVPC
	if err := json.Unmarshal(apiResp.Body, &vpc); err != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Response", err.Error())
		return
	}

	state.fromAPI(ctx, &vpc, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan VPCModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state VPCModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// PUT, not PATCH. The network service registers PUT /vpcs/{id} for the
	// in-place name/description/tags update (vpc_handler.go), and the gateway
	// routes it there — `network-vpcs`, methods GET/PUT/OPTIONS. PATCH is
	// registered by NOBODY: not by network, and not by provisioning, which owns
	// only the POST/DELETE lifecycle. So the PATCH this used to send matched no
	// gateway rule at all and came back PATH_NOT_ROUTED, which made
	// frostmoln_vpc a create/delete-only resource — any name, description or
	// tag change was unappliable, and combined with a description the platform
	// did not persist, unrecoverable without a destroy/recreate.
	//
	// The same split applies to subnets, security groups and public IPs: their
	// in-place update is a synchronous PUT on network, never a workflow write.
	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", state.ID.ValueString())), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update VPC", err.Error())
		return
	}

	var vpc apiVPC
	if err := json.Unmarshal(apiResp.Body, &vpc); err != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &vpc, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VPCModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	subject := state.ID.ValueString()
	budgets := r.resolveBudgets(state.Timeouts)

	query := url.Values{}
	query.Set("force", "true")

	delResp, err := r.client.DeleteWithQuery(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", subject)), query)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete VPC", err.Error())
		return
	}

	// A VPC delete routes through provisioning, which answers 202 with an
	// Operation envelope BEFORE the platform has decided anything — and the
	// destroy of an async backend used to be reported as done the moment that
	// 202 landed, so a delete whose workflow later FAILED still dropped the
	// state row while the VPC (and everything in it) stayed alive. Parse the
	// envelope and wait for the workflow's verdict; a non-202 is a synchronous
	// backend and needs no watch.
	if !delResp.IsAccepted() {
		return
	}
	op, opErr := client.ParseResponse[client.Operation](delResp)
	if opErr != nil || op.OperationID == "" {
		// The destroy was accepted and its workflow cannot be watched from
		// here. That is classified — NOT a success and NOT a verified absence.
		unwatched := opErr
		if unwatched == nil {
			unwatched = fmt.Errorf("the delete was accepted but returned no operation id")
		}
		orphan.AddDeleteOutcome(&resp.Diagnostics, client.OperationUnknown, "VPC", subject, unwatched)
		return
	}
	if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), budgets.Delete); waitErr != nil {
		orphan.AddDeleteOutcome(&resp.Diagnostics,
			r.client.ClassifyOperationFailure(ctx, op.OperationID),
			"VPC", subject, waitErr)
	}
}

func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
