package messaging_permission

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &permissionResource{}
	_ resource.ResourceWithImportState = &permissionResource{}
	_ resource.ResourceWithModifyPlan  = &permissionResource{}
)

// NewResource returns a new frostmoln_messaging_permission resource factory.
func NewResource() resource.Resource {
	return &permissionResource{}
}

type permissionResource struct {
	client *client.Client
	// pollInterval is the test seam; zero means messagingobj.DefaultPollInterval.
	pollInterval time.Duration
}

func (r *permissionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_messaging_permission"
}

func regexAttribute(what string) schema.StringAttribute {
	return schema.StringAttribute{
		Description: "The regular expression of resource names the user may " + what + " on the vhost, as LavinMQ " +
			"applies it: at most 255 bytes, never starting with `-`. `.*` matches everything; `^$` grants " +
			"nothing. Changing it updates the grant in place.",
		Required: true,
		Validators: []validator.String{
			stringvalidator.LengthBetween(1, 255),
			stringvalidator.RegexMatches(regexp.MustCompile(`^[^-]`), "must not start with \"-\""),
		},
	}
}

func identityAttribute(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		Description: desc,
		Required:    true,
		PlanModifiers: []planmodifier.String{
			stringplanmodifier.RequiresReplace(),
		},
		Validators: messagingobj.NameValidators(),
	}
}

func (r *permissionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages one application user's permission on one vhost of a managed messaging (LavinMQ) " +
			"instance: three regular expressions saying which exchanges and queues the user may configure, " +
			"write to and read from.\n\n" +
			"Create, update and destroy wait until the platform has applied the change on the broker. When a " +
			"change fails the broker keeps the previous grant, the apply fails, and the regexes in state are the " +
			"ones the broker holds — so the next plan shows the change again. Both the user and the vhost must " +
			"be `ready` first: reference the `frostmoln_messaging_user` and `frostmoln_messaging_vhost` " +
			"resources so Terraform orders it. Operations on one instance run one at a time, so the provider " +
			"serialises every vhost, user and permission change per instance.\n\n" +
			"`create_before_destroy` is not supported: the replacement names the same user and vhost as the grant " +
			"it replaces, which still exists, so its create is refused.\n\n" +
			"Import with `<instance_id>/<user>/<vhost>`." +
			"\n\n" + scopedecl.Summary("frostmoln_messaging_permission"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The permission's Terraform id, `<instance_id>/<user>/<vhost>`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The id of the `frostmoln_messaging_instance` the user and the vhost live on. Changing it " +
					"replaces the permission.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: messagingobj.InstanceIDValidators(),
			},
			"user": identityAttribute("The name of the `frostmoln_messaging_user` granted access. Changing it " +
				"replaces the permission."),
			"vhost": identityAttribute("The name of the `frostmoln_messaging_vhost` the access is to. Changing it " +
				"replaces the permission."),
			"configure": regexAttribute("create, delete and change"),
			"write":     regexAttribute("publish to"),
			"read":      regexAttribute("consume from"),
			"status": schema.StringAttribute{
				Description: "The permission's status: `pending` while the platform applies a change, `ready`, " +
					"`deleting`, or `error` when a create failed (the grant is absent or its state unknown; the next " +
					"apply deletes and re-creates it).",
				Computed: true,
			},
			"failure_reason": schema.StringAttribute{
				Description: "The stable code of the permission's last failed change (for example `IN_FLIGHT` or " +
					"`BROKER_REFUSED`), or null. Every code but `OUTCOME_UNKNOWN` means the broker was left unchanged.",
				Computed: true,
			},
			"failure_message": schema.StringAttribute{
				Description: "The last failed change's reason in words, or null.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "When the permission was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the permission last changed.",
				Computed:    true,
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Schema(),
		},
	}
}

func (r *permissionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func subject(instanceID, user, vhost string) string {
	return fmt.Sprintf("The permission of user %q on vhost %q (messaging instance %s)", user, vhost, instanceID)
}

func (r *permissionResource) path(m *permissionModel) string {
	return messagingobj.PermissionPath(r.client, m.InstanceID.ValueString(), m.User.ValueString(), m.Vhost.ValueString())
}

func (r *permissionResource) get(ctx context.Context, m *permissionModel) (*messagingobj.Permission, error) {
	apiResp, err := r.client.Get(ctx, r.path(m), nil)
	if err != nil {
		return nil, err
	}
	p, err := client.ParseResponse[messagingobj.Permission](apiResp)
	if err != nil {
		return nil, err
	}
	if !samePermission(m.User.ValueString(), m.Vhost.ValueString(), p) {
		return nil, messagingobj.Mismatch("requested the permission of %q on %q but the API answered with %q on %q",
			m.User.ValueString(), m.Vhost.ValueString(), p.User, p.Vhost)
	}
	return p, nil
}

func (r *permissionResource) wait(ctx context.Context, m *permissionModel, budget time.Duration) (*messagingobj.Permission, bool, error) {
	return messagingobj.Wait(ctx, r.pollInterval, budget, "messaging permission",
		func(ctx context.Context) (*messagingobj.Permission, error) { return r.get(ctx, m) },
		func(p *messagingobj.Permission) string { return p.Status })
}

func (r *permissionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	ctx = messagingobj.English(ctx)
	var plan permissionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	subj := subject(plan.InstanceID.ValueString(), plan.User.ValueString(), plan.Vhost.ValueString())

	release, err := messagingobj.Lock(ctx, plan.InstanceID.ValueString())
	if err != nil {
		messagingobj.AddLockError(&resp.Diagnostics, err)
		return
	}
	defer release()

	// The PUT creates OR REPLACES, so without this check a create would silently
	// take over — and rewrite — a grant made outside this configuration.
	if _, err := r.get(ctx, &plan); err == nil {
		resp.Diagnostics.AddError("The messaging permission already exists",
			subj+" already exists. Import it with `terraform import` (id `<instance_id>/<user>/<vhost>`) to manage it, "+
				"rather than overwrite it.")
		return
	} else if !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to check for an existing messaging permission", err.Error())
		return
	}

	apiResp, err := r.client.Put(ctx, r.path(&plan), plan.request())
	if err != nil {
		messagingobj.AddRefusal(&resp.Diagnostics, "Failed to create the messaging permission", subj, err)
		return
	}
	staged, _ := client.ParseResponse[messagingobj.Permission](apiResp)

	p, gone, waitErr := r.wait(ctx, &plan, messagingobj.Budgets(plan.Timeouts).Create)
	if p == nil {
		p = staged
	}
	switch {
	case gone:
		resp.Diagnostics.AddError("The messaging permission disappeared while it was being created",
			subj+" was deleted before its create finished — most likely by another client. Nothing was recorded; "+
				"apply again to create it.")
		return
	case waitErr != nil:
		if p != nil {
			plan.fromAPI(p)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
		resp.Diagnostics.AddError("The messaging permission did not finish creating",
			fmt.Sprintf("%s: %s. It is recorded with its last known status and marked tainted, so the next apply "+
				"deletes and re-creates it; if a refresh shows it `ready`, `terraform untaint` keeps it instead.",
				subj, waitErr.Error()))
		return
	}

	plan.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	switch messagingobj.Judge(p.Status, p.FailureReason) {
	case messagingobj.Errored:
		resp.Diagnostics.AddError("Failed to create the messaging permission",
			fmt.Sprintf("%s could not be created: %s. %s It is recorded as tainted, so the next apply deletes the "+
				"failed grant (locally, when it never reached the broker) and creates it again.",
				subj, messagingobj.FailureText(p.FailureReason, p.FailureMessage), messagingobj.BrokerSentence(p.FailureReason)))
	case messagingobj.ChangeFailed:
		resp.Diagnostics.AddError("Failed to create the messaging permission",
			fmt.Sprintf("%s reads `ready` but its last change failed: %s. %s",
				subj, messagingobj.FailureText(p.FailureReason, p.FailureMessage), messagingobj.BrokerSentence(p.FailureReason)))
	}
}

func (r *permissionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	ctx = messagingobj.English(ctx)
	var state permissionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.get(ctx, &state)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read the messaging permission", err.Error())
		return
	}
	state.fromAPI(p)
	messagingobj.WarnIfErrored(&resp.Diagnostics,
		subject(state.InstanceID.ValueString(), state.User.ValueString(), state.Vhost.ValueString()),
		p.Status, p.FailureReason, p.FailureMessage)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update replaces the grant's regexes with a PUT and waits for the verdict.
// EVERY failure path records what the platform holds (or, failing that, the
// prior state) instead of leaving the response's default — the PLAN — in
// place: Terraform persists the state an errored Update returns, and the plan
// there would claim a grant the broker was never given.
func (r *permissionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	ctx = messagingobj.English(ctx)
	var plan, state permissionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	subj := subject(state.InstanceID.ValueString(), state.User.ValueString(), state.Vhost.ValueString())
	// The timeouts block is the one configurable thing that is not the grant
	// (instance_id, user and vhost replace).
	state.Timeouts = plan.Timeouts
	keepActual := func(p *messagingobj.Permission) {
		if p == nil {
			if got, err := r.get(ctx, &state); err == nil {
				p = got
			}
		}
		if p != nil {
			state.fromAPI(p)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	}

	if plan.request() == state.request() {
		// Only the timeouts block changed: nothing to send.
		keepActual(nil)
		return
	}

	release, err := messagingobj.Lock(ctx, state.InstanceID.ValueString())
	if err != nil {
		messagingobj.AddLockError(&resp.Diagnostics, err)
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	defer release()

	if _, err := r.client.Put(ctx, r.path(&state), plan.request()); err != nil {
		messagingobj.AddRefusal(&resp.Diagnostics, "Failed to update the messaging permission", subj, err)
		keepActual(nil)
		return
	}

	p, gone, waitErr := r.wait(ctx, &state, messagingobj.Budgets(plan.Timeouts).Update)
	switch {
	case gone:
		resp.State.RemoveResource(ctx)
		resp.Diagnostics.AddError("The messaging permission disappeared while it was being updated",
			subj+" was deleted during the update — most likely by another client. It was removed from state; apply "+
				"again to create it.")
		return
	case waitErr != nil:
		keepActual(p)
		resp.Diagnostics.AddError("The messaging permission did not finish updating",
			fmt.Sprintf("%s: %s. State holds the grant the broker was last given, so the next plan shows the "+
				"change again.", subj, waitErr.Error()))
		return
	}

	keepActual(p)
	if outcome := messagingobj.Judge(p.Status, p.FailureReason); outcome != messagingobj.Succeeded {
		resp.Diagnostics.AddError("Failed to update the messaging permission",
			fmt.Sprintf("%s was not changed: %s. %s State holds the grant the broker was last given, so the next "+
				"plan shows the change again.",
				subj, messagingobj.FailureText(p.FailureReason, p.FailureMessage), messagingobj.BrokerSentence(p.FailureReason)))
	}
}

func (r *permissionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	ctx = messagingobj.English(ctx)
	var state permissionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	subj := subject(state.InstanceID.ValueString(), state.User.ValueString(), state.Vhost.ValueString())

	release, err := messagingobj.Lock(ctx, state.InstanceID.ValueString())
	if err != nil {
		messagingobj.AddLockError(&resp.Diagnostics, err)
		return
	}
	defer release()

	apiResp, err := r.client.Delete(ctx, r.path(&state))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		messagingobj.AddRefusal(&resp.Diagnostics, "Failed to delete the messaging permission", subj, err)
		return
	}
	if apiResp.StatusCode == http.StatusNoContent {
		return
	}

	p, gone, waitErr := r.wait(ctx, &state, messagingobj.Budgets(state.Timeouts).Delete)
	if gone {
		return
	}
	if waitErr != nil {
		resp.Diagnostics.AddError("The messaging permission did not finish deleting",
			fmt.Sprintf("%s: %s. It stays in state; apply again to finish the destroy.", subj, waitErr.Error()))
		return
	}
	resp.Diagnostics.AddError("The messaging permission was NOT deleted",
		fmt.Sprintf("%s reads `%s` after its delete: %s. %s The user still holds the grant. It stays in state; "+
			"apply again to retry.",
			subj, p.Status, messagingobj.FailureText(p.FailureReason, p.FailureMessage), messagingobj.BrokerSentence(p.FailureReason)))
}

// ImportState takes "<instance_id>/<user>/<vhost>".
func (r *permissionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "instance_id", "user", "vhost")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vhost"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+parts[1]+"/"+parts[2])...)
}

// ModifyPlan replaces an object whose state says `error` (a failed create),
// even when it is not tainted — after an import or `terraform untaint`.
func (r *permissionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	messagingobj.ReplaceWhenErrored(ctx, req, resp)
}
