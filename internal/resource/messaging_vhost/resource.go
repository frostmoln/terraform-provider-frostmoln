package messaging_vhost

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

var (
	_ resource.Resource                = &vhostResource{}
	_ resource.ResourceWithImportState = &vhostResource{}
	_ resource.ResourceWithModifyPlan  = &vhostResource{}
)

// NewResource returns a new frostmoln_messaging_vhost resource factory.
func NewResource() resource.Resource {
	return &vhostResource{}
}

type vhostResource struct {
	client *client.Client
	// pollInterval is the test seam; zero means messagingobj.DefaultPollInterval.
	pollInterval time.Duration
}

func (r *vhostResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_messaging_vhost"
}

func (r *vhostResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a virtual host (vhost) on a managed messaging (LavinMQ) instance. A vhost is an " +
			"isolated namespace of exchanges, queues and bindings; grant a `frostmoln_messaging_user` access to " +
			"it with a `frostmoln_messaging_permission`.\n\n" +
			"Create and destroy wait until the platform has applied the change on the broker. Operations on one " +
			"instance run one at a time, so the provider serialises every vhost, user and permission change per " +
			"instance (other instances still proceed in parallel). Destroying a vhost DELETES EVERY MESSAGE IN " +
			"IT, and is refused while any permission still names it.\n\n" +
			"Import with `<instance_id>/<name>`." +
			"\n\n" + scopedecl.Summary("frostmoln_messaging_vhost"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The vhost's Terraform id, `<instance_id>/<name>`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The id of the `frostmoln_messaging_instance` the vhost lives on. Changing it creates the " +
					"vhost on the other instance and deletes this one.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: messagingobj.InstanceIDValidators(),
			},
			"name": schema.StringAttribute{
				Description: "The vhost's name: " + messagingobj.NameRule + ". Changing it replaces the vhost and " +
					"deletes every message in the old one.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: messagingobj.NameValidators(),
			},
			"status": statusAttribute(),
			"failure_reason": schema.StringAttribute{
				Description: "The stable code of the vhost's last failed change (for example `IN_FLIGHT` or " +
					"`BROKER_REFUSED`), or null. Every code but `OUTCOME_UNKNOWN` means the broker was left unchanged.",
				Computed: true,
			},
			"failure_message": schema.StringAttribute{
				Description: "The last failed change's reason in words, or null.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "When the vhost was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "When the vhost last changed.",
				Computed:    true,
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Schema(),
		},
	}
}

func statusAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Description: "The vhost's status: `pending` while the platform applies a create, `ready`, `deleting`, or " +
			"`error` when a create failed (the vhost is absent or its state unknown; the next apply deletes and " +
			"re-creates it).",
		Computed: true,
	}
}

func (r *vhostResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func subject(instanceID, name string) string {
	return fmt.Sprintf("Vhost %q on messaging instance %s", name, instanceID)
}

func (r *vhostResource) get(ctx context.Context, instanceID, name string) (*messagingobj.Object, error) {
	apiResp, err := r.client.Get(ctx, messagingobj.VhostPath(r.client, instanceID, name), nil)
	if err != nil {
		return nil, err
	}
	v, err := client.ParseResponse[messagingobj.Object](apiResp)
	if err != nil {
		return nil, err
	}
	// Never let a response rewrite the identity of the object being managed.
	if !sameName(name, v.Name) {
		return nil, messagingobj.Mismatch("requested vhost %q but the API answered with %q", name, v.Name)
	}
	return v, nil
}

func (r *vhostResource) wait(ctx context.Context, instanceID, name string, budget time.Duration) (*messagingobj.Object, bool, error) {
	return messagingobj.Wait(ctx, r.pollInterval, budget, "messaging vhost",
		func(ctx context.Context) (*messagingobj.Object, error) { return r.get(ctx, instanceID, name) },
		func(v *messagingobj.Object) string { return v.Status })
}

func (r *vhostResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	ctx = messagingobj.English(ctx)
	var plan vhostModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	instanceID, name := plan.InstanceID.ValueString(), plan.Name.ValueString()
	subj := subject(instanceID, name)

	release, err := messagingobj.Lock(ctx, instanceID)
	if err != nil {
		messagingobj.AddLockError(&resp.Diagnostics, err)
		return
	}
	defer release()

	apiResp, err := r.client.Post(ctx, messagingobj.VhostsPath(r.client, instanceID), messagingobj.CreateRequest{Name: name})
	if err != nil {
		messagingobj.AddRefusal(&resp.Diagnostics, "Failed to create the messaging vhost", subj, err)
		return
	}
	// The 202 body is the staged vhost: the fallback record if no poll lands.
	staged, _ := client.ParseResponse[messagingobj.Object](apiResp)

	v, gone, waitErr := r.wait(ctx, instanceID, name, messagingobj.Budgets(plan.Timeouts).Create)
	if v == nil {
		v = staged
	}
	switch {
	case gone:
		resp.Diagnostics.AddError("The messaging vhost disappeared while it was being created",
			subj+" was deleted before its create finished — most likely by another client. Nothing was recorded; "+
				"apply again to create it.")
		return
	case waitErr != nil:
		if v != nil {
			// Accepted and still in flight: keep it tracked (tainted) rather
			// than orphan it.
			plan.fromAPI(v)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
		resp.Diagnostics.AddError("The messaging vhost did not finish creating",
			fmt.Sprintf("%s: %s. It is recorded with its last known status and marked tainted, so the next apply "+
				"deletes and re-creates it; if a refresh shows it `ready`, `terraform untaint` keeps it instead.",
				subj, waitErr.Error()))
		return
	}

	plan.fromAPI(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	switch messagingobj.Judge(v.Status, v.FailureReason) {
	case messagingobj.Errored:
		resp.Diagnostics.AddError("Failed to create the messaging vhost",
			fmt.Sprintf("%s could not be created: %s. %s It is recorded as tainted, so the next apply deletes the "+
				"failed vhost (locally, when it never reached the broker) and creates it again.",
				subj, messagingobj.FailureText(v.FailureReason, v.FailureMessage), messagingobj.BrokerSentence(v.FailureReason)))
	case messagingobj.ChangeFailed:
		resp.Diagnostics.AddError("Failed to create the messaging vhost",
			fmt.Sprintf("%s reads `ready` but its last change failed: %s. %s",
				subj, messagingobj.FailureText(v.FailureReason, v.FailureMessage), messagingobj.BrokerSentence(v.FailureReason)))
	}
}

func (r *vhostResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	ctx = messagingobj.English(ctx)
	var state vhostModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.get(ctx, state.InstanceID.ValueString(), state.Name.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read the messaging vhost", err.Error())
		return
	}
	state.fromAPI(v)
	messagingobj.WarnIfErrored(&resp.Diagnostics, subject(state.InstanceID.ValueString(), state.Name.ValueString()),
		v.Status, v.FailureReason, v.FailureMessage)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is reached only by a change to the timeouts block: every
// configurable attribute replaces. Nothing is sent; the vhost is re-read.
func (r *vhostResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	ctx = messagingobj.English(ctx)
	var plan, state vhostModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringValue(plan.InstanceID.ValueString() + "/" + plan.Name.ValueString())
	plan.Status, plan.FailureReason = state.Status, state.FailureReason
	plan.FailureMessage, plan.CreatedAt, plan.UpdatedAt = state.FailureMessage, state.CreatedAt, state.UpdatedAt
	if v, err := r.get(ctx, plan.InstanceID.ValueString(), plan.Name.ValueString()); err == nil {
		plan.fromAPI(v)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vhostResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	ctx = messagingobj.English(ctx)
	var state vhostModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	instanceID, name := state.InstanceID.ValueString(), state.Name.ValueString()
	subj := subject(instanceID, name)

	release, err := messagingobj.Lock(ctx, instanceID)
	if err != nil {
		messagingobj.AddLockError(&resp.Diagnostics, err)
		return
	}
	defer release()

	apiResp, err := r.client.Delete(ctx, messagingobj.VhostPath(r.client, instanceID, name))
	if err != nil {
		switch {
		case client.IsNotFound(err):
			return
		case messagingobj.HasCode(err, messagingobj.CodeObjectInUse):
			resp.Diagnostics.AddError("The messaging vhost is still in use",
				messagingobj.InUseDetail(ctx, r.client, messagingobj.VhostPermissionsPath(r.client, instanceID, name),
					subj, "users", func(p messagingobj.Permission) string { return p.User })+"\n\nError: "+err.Error())
		default:
			messagingobj.AddRefusal(&resp.Diagnostics, "Failed to delete the messaging vhost", subj, err)
		}
		return
	}
	if apiResp.StatusCode == http.StatusNoContent {
		// Removed locally: the broker never held it.
		return
	}

	v, gone, waitErr := r.wait(ctx, instanceID, name, messagingobj.Budgets(state.Timeouts).Delete)
	if gone {
		return
	}
	if waitErr != nil {
		resp.Diagnostics.AddError("The messaging vhost did not finish deleting",
			fmt.Sprintf("%s: %s. It stays in state; apply again to finish the destroy.", subj, waitErr.Error()))
		return
	}
	resp.Diagnostics.AddError("The messaging vhost was NOT deleted",
		fmt.Sprintf("%s reads `%s` after its delete: %s. %s It stays in state; apply again to retry.",
			subj, v.Status, messagingobj.FailureText(v.FailureReason, v.FailureMessage), messagingobj.BrokerSentence(v.FailureReason)))
}

// ImportState takes "<instance_id>/<name>".
func (r *vhostResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "instance_id", "name")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[0]+"/"+parts[1])...)
}

// ModifyPlan replaces an object whose state says `error` (a failed create),
// even when it is not tainted — after an import or `terraform untaint`.
func (r *vhostResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	messagingobj.ReplaceWhenErrored(ctx, req, resp)
}
