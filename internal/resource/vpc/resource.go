package vpc

import (
	"context"
	"encoding/json"
	"errors"
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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &vpcResource{}
	_ resource.ResourceWithImportState = &vpcResource{}
	_ resource.ResourceWithModifyPlan  = &vpcResource{}
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
				Description: "Tags for the VPC." + tftags.TagsNote,
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"tags_all": tftags.TagsAllAttribute(),
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
			"dns_label": schema.StringAttribute{
				Description: "The VPC's DNS label, the first part of its internal DNS domain. " +
					"Unset, the platform derives it from `name` (diacritics transliterated, lowercased, " +
					"other characters become `-`, at most 63; `vpc` if nothing is left) and a derived " +
					"label never fails: one already used in the tenant gets `-2`, `-3`. Set explicitly, " +
					"it must be an RFC 1123 label (a-z, 0-9, `-`, at most 63) free in the tenant, or the " +
					"create fails with `VPCDNSLabelTaken`.\n\n" +
					"    **Fixed at create.** Renaming the VPC never changes it, and a change to this " +
					"attribute is REFUSED (a warning at plan, an error at apply, before anything is sent) " +
					"rather than planned as a replacement: replacing a VPC destroys everything in it. To " +
					"use another label, create a new VPC.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"dns_domain": schema.StringAttribute{
				Description: "The VPC's internal DNS domain, `<dns_label>.<tenant>.<org>.fm.internal.` " +
					"(with a trailing dot; the tenant and organization parts are opaque ids, never names). " +
					"Instances in the VPC resolve as `<hostname>.<dns_domain>` from inside this VPC only. " +
					"Assigned by the platform and fixed once assigned; null until then.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			// RETIRED (2026-10-23): VM DNS names are always published, so the
			// switch is gone from the platform. The attribute is KEPT, deprecated
			// and inert, because removing it turns every configuration that sets
			// it into an "Unsupported argument" error at parse time. It is never
			// sent and never read back: state holds the configured value, or null
			// when unset (retiredHostnamesPlan), so neither setting it, keeping a
			// value written by an older provider, nor deleting the line from HCL
			// ever plans a change or an inconsistent result.
			"enable_dns_hostnames": schema.BoolAttribute{
				Description: "**Deprecated and ignored.** VM DNS names are always published: every VM in " +
					"the VPC gets its `hostname` / `fqdn` (see `frostmoln_instance`). The value is never " +
					"sent to the platform; state keeps the configured value, or null when unset. Remove " +
					"the attribute from your configuration — doing so plans no change.",
				DeprecationMessage: "enable_dns_hostnames is retired and ignored: VM DNS names are always " +
					"published. Remove the attribute from your configuration; doing so plans no change.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Bool{
					retiredHostnamesPlan{},
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

	defaults := r.client.DefaultTags()
	createReq := plan.toCreateRequest()
	createReq.Tags = tftags.ForCreate(ctx, defaults, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/vpcs"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create VPC", err.Error())
		return
	}
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

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
				if operationErrorCode(waitErr) == errCodeVPCDNSLabelTaken {
					resp.Diagnostics.AddAttributeError(path.Root("dns_label"),
						"VPC DNS Label Already Taken",
						fmt.Sprintf("Another VPC in this tenant already uses the DNS label %q; labels are unique "+
							"per tenant. Nothing was created and nothing has been recorded in Terraform state.\n\n"+
							"Choose another `dns_label`, or remove it to have one derived from `name` (a derived "+
							"label never collides).\n\nThe platform said: %s", plan.DNSLabel.ValueString(), waitErr))
					return
				}
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
	tftags.FinishRead(ctx, req.Private, resp.Private, r.client.DefaultTags(), &state.Tags, state.TagsAll, &resp.Diagnostics)
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

	// The apply-time half of the dns_label refusal (see dnsLabelChange):
	// refused before anything is sent, so the VPC is left exactly as it was.
	if summary, detail, refused := dnsLabelChange(plan.DNSLabel, state.DNSLabel); refused {
		resp.Diagnostics.AddAttributeError(path.Root("dns_label"), summary, detail)
		return
	}

	defaults := r.client.DefaultTags()
	updateReq := plan.toUpdateRequest()
	current, err := r.currentTags(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", state.ID.ValueString())))
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Current Tags", tftags.CurrentTagsReadFailed+err.Error())
		return
	}
	prior := tftags.PriorOf(ctx, state.Tags, state.TagsAll, req.Private, &resp.Diagnostics).WithCurrent(current)
	updateReq.Tags, _ = tftags.ForUpdate(ctx, defaults, plan.Tags, prior, &resp.Diagnostics)
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
	tftags.RecordDefaults(ctx, resp.Private, defaults, &resp.Diagnostics)

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
	// The retired enable_dns_hostnames is never read from the API, so Read
	// cannot fill it. Import records the truthful value (VM DNS names are
	// always on), so a configuration that sets it to true or leaves it unset
	// plans clean right after the import (retiredHostnamesPlan keeps state).
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("enable_dns_hostnames"), true)...)
	tftags.MarkImported(ctx, resp.Private, &resp.Diagnostics)
}

// ModifyPlan predicts tags_all: unknown whenever the next write changes the
// platform's tag set, including a change to the provider's default_tags. It
// also WARNS about a dns_label change, which Update then refuses.
func (r *vpcResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	warnDNSLabelChange(ctx, req, resp)
	tftags.PlanTagsAll(ctx, r.client.DefaultTags(), req, resp)
}

// dnsLabelChange: a dns_label change is REFUSED, never planned as a
// replacement.
//
// NEVER RequiresReplace. The label is frozen by the platform, so the only way
// to "change" it is a new VPC — and replacing a VPC destroys every subnet,
// instance and managed service in it to rename a DNS zone. A refusal names the
// conflict and leaves the choice (a new VPC, or putting the label back) with
// the practitioner; scopedecl declares it ImmutableWithoutReplace.
//
// WARNED AT PLAN, REFUSED AT APPLY (the frostmoln_secret pattern). An error in
// ModifyPlan would also abort `terraform destroy` — its refresh phase plans
// against the configuration — so the plan shows a warning that the apply will
// fail, and Update refuses before it sends anything
// (TestNoErrorDiagnosticsWhilePlanning holds the rule provider-wide).
//
// Only a CONFIGURED value is judged: unset, the attribute plans as the state
// value (UseStateForUnknown) and there is nothing to refuse.
//
// dnsLabelChange returns that refusal for a planned dns_label against the
// VPC's current one, or refused=false when there is nothing to refuse.
func dnsLabelChange(planned, current types.String) (summary, detail string, refused bool) {
	if planned.IsNull() || planned.IsUnknown() {
		return "", "", false
	}
	if current.IsNull() || current.IsUnknown() || current.ValueString() == "" {
		return "VPC DNS Label Cannot Be Set On An Existing VPC",
			fmt.Sprintf("This VPC has no DNS label yet. The platform assigns one to a VPC created before "+
				"labels existed, and a label can only be chosen at create. Remove `dns_label` (= %q) from the "+
				"configuration; once the platform has labelled the VPC, a refresh shows the label it chose.",
				planned.ValueString()), true
	}
	if planned.ValueString() == current.ValueString() {
		return "", "", false
	}
	return "VPC DNS Label Cannot Change",
		fmt.Sprintf("The VPC's DNS label is %q and is fixed at create: renaming the VPC never moves it, and "+
			"the platform refuses to change it. Terraform will not replace the VPC to get %q either, because "+
			"replacing a VPC destroys everything in it.\n\n"+
			"Set `dns_label = %q` (or remove it), or create a new VPC with the label you want.",
			current.ValueString(), planned.ValueString(), current.ValueString()), true
}

// warnDNSLabelChange is the plan-time half: a warning, never an error.
func warnDNSLabelChange(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // create or destroy
	}
	var configured, current types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("dns_label"), &configured)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("dns_label"), &current)...)
	if summary, detail, refused := dnsLabelChange(configured, current); refused {
		resp.Diagnostics.AddAttributeWarning(path.Root("dns_label"), summary,
			detail+"\n\nThis apply will fail; a destroy is unaffected.")
	}
}

// operationErrorCode is the published errorCode of a failed operation in
// err's chain, or "" when it carries none.
func operationErrorCode(err error) string {
	var opErr *client.OperationError
	if errors.As(err, &opErr) {
		return opErr.ErrorCode
	}
	return ""
}

// currentTags reads the tags the platform holds right now, at memberPath. An update
// derives the keys it keeps — the ones this configuration does not manage —
// from this rather than from state, which is only as fresh as the last
// refresh (tftags.Prior.WithCurrent).
func (r *vpcResource) currentTags(ctx context.Context, memberPath string) (map[string]string, error) {
	apiResp, err := r.client.Get(ctx, memberPath, nil)
	if err != nil {
		return nil, err
	}
	obj, err := client.ParseResponse[apiVPC](apiResp)
	if err != nil {
		return nil, err
	}
	return obj.customerTags(), nil
}

// retiredHostnamesPlan plans the retired enable_dns_hostnames so it never
// shows a diff or an unknown it does not have to: a configured value plans as
// itself (the framework already did that); an unset one plans as the prior
// state value (null on create). It replaces UseStateForUnknown, which on
// create would leave an unset value "(known after apply)".
type retiredHostnamesPlan struct{}

func (retiredHostnamesPlan) Description(_ context.Context) string {
	return "retired: keeps the configured value, else the value already in state (null on create)"
}

func (m retiredHostnamesPlan) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (retiredHostnamesPlan) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	// A known planned value comes from config; an unknown config value is an
	// interpolation that resolves before apply.
	if !req.PlanValue.IsUnknown() || req.ConfigValue.IsUnknown() {
		return
	}
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.BoolNull()
		return
	}
	resp.PlanValue = req.StateValue
}
