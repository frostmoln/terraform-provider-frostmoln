package instance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
)

var (
	_ resource.Resource                = &instanceResource{}
	_ resource.ResourceWithImportState = &instanceResource{}
)

// NewResource returns a new instance resource factory.
func NewResource() resource.Resource {
	return &instanceResource{}
}

type instanceResource struct {
	client       *client.Client
	pollInterval time.Duration // overridable for tests; defaults to 5s
	pollTimeout  time.Duration // overridable for tests; defaults to 10m
}

func (r *instanceResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *instanceResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 10 * time.Minute
}

// getResizeTimeout is the budget for a resize operation specifically. A resize is
// the longest instance operation there is and the generic 10-minute instance
// timeout is smaller than the backend's own worst case: provisioning waits up to
// 10 minutes for VERIFY_RESIZE inside a 30-minute LongRunning activity with
// retries, then another 5 for the confirm to settle — a cross-host cold migration
// of a large VM legitimately outruns 10 minutes. Declaring failure early is not
// free: the workflow completes anyway, so Terraform keeps the OLD flavor_id while
// the platform (and the quota ledger) moves to the new one, and a re-apply against
// a stale plan is refused with "cannot resize to the same flavor". The portal made
// the same correction for the same endpoint (services/compute.ts resizeInstance,
// 20 min); this is sized against the server budget rather than matched to it.
// Overridden by pollTimeout when a test sets one.
func (r *instanceResource) getResizeTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 45 * time.Minute
}

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a compute instance in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the instance.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the instance.",
				Required:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID for the instance. Changing this resizes the instance in place: the platform powers it down for the migration and brings it back up. It does not power on an instance that was already stopped, and a guest that fails to come back up after the migration leaves the instance stopped with the resize still reported as successful — check `status` after a resize.",
				Required:    true,
			},
			"image_id": schema.StringAttribute{
				Description: "The image ID to use for the instance.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"zone": schema.StringAttribute{
				// Optional+Computed: when config omits it the platform picks an AZ
				// and echoes it, so an Optional-only attr would fail every zone-less
				// apply with "provider produced inconsistent result" (null plan, non-null
				// read-back). UseStateForUnknown pins the recorded AZ so a follow-up plan
				// stays empty. Matches subnet's zone/gateway_ip.
				Description: "The availability zone for the instance. If omitted, the platform selects one and records it in state.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID for the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID for the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"security_groups": schema.SetAttribute{
				// Updated IN PLACE via PUT /instances/{id}/security-groups (replace
				// semantics, by Neutron SG UUID). Plain Optional (no RequiresReplace):
				// a change runs Update, which applies the new set and waits for the
				// async apply to converge. State keeps the configured set (preserved
				// from plan in fromAPI, since the instance read returns SG NAMES, not
				// the UUIDs the user supplied — same identifier-space reason as before).
				Description: "The security group IDs attached to the instance. Updated in place (replace semantics): changing the set replaces the instance's security groups across all its ports. Setting it to [] or removing the attribute clears ALL security groups (the instance falls back to default-drop — typically no inbound access). Out-of-band changes (made via the portal, CLI, or another client) are detected as drift on refresh when every port shares the same set; if ports hold differing sets, the configured value is preserved and a warning is emitted (edit per port instead).",
				Optional:    true,
				ElementType: types.StringType,
			},
			"ssh_key_names": schema.SetAttribute{
				Description: "The SSH key names to inject into the instance.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
			},
			"user_data": schema.StringAttribute{
				Description: "User data to provide to the instance at launch — typically a cloud-init " +
					"document. The API does not return it, so the value you configure is preserved from " +
					"state on refresh and a SHA256 hash is stored alongside it for change detection. " +
					docs.UserDataStateNote + " Prefer `user_data_wo`, which carries the same document but " +
					"is never written to state; the two are mutually exclusive.\n\n" +
					"**Write the document as plain text — `file(\"cloud-init.yaml\")`, not " +
					"`base64encode(file(...))`.** Base64 is accepted by the API, but it must NOT be " +
					"used on an instance that also sets `ssh_key_names`, `console_password` or " +
					"`instance_access`. In those cases the platform merges its own cloud-config into " +
					"the document, and the merge dispatches on the literal `#cloud-config` prefix: a " +
					"base64 blob does not carry it, so the blob is treated as a shell script and " +
					"combined alongside the platform's cloud-config instead of into it. The apply " +
					"succeeds and the plan stays clean, but the document never runs as cloud-config " +
					"and the only evidence is in the guest's cloud-init log. Plain text is correct in " +
					"both directions: a `#cloud-config` document is merged in place, and a `#!` script " +
					"is combined as intended. See the example below.\n\n" +
					"The hash is taken over the value AS WRITTEN in the configuration, and any change " +
					"to `user_data` forces the instance to be REPLACED — so moving a live instance off " +
					"`base64encode(file(...))` onto `file(...)` plans a replacement even though the " +
					"document itself is unchanged. Worth doing, but do it deliberately.\n\n" +
					"A cloud-init step that installs packages or calls an external endpoint also needs " +
					"the instance's VPC to have an outbound path: declare a `frostmoln_gateway` for the " +
					"VPC, or the step fails on first boot with no internet and no name resolution.",
				Optional:  true,
				Sensitive: true,
				Validators: []validator.String{
					stringvalidator.PreferWriteOnlyAttribute(path.MatchRoot("user_data_wo")),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_data_wo": schema.StringAttribute{
				Description: "User data to provide to the instance at launch, as a [write-only argument]" +
					"(https://developer.hashicorp.com/terraform/language/resources/ephemeral/write-only): the " +
					"document reaches the provider on apply and is never written to the plan or to state, so " +
					"anything embedded in it stays out of the state file. Requires Terraform 1.11 or later. " +
					"Mutually exclusive with `user_data`, and `user_data_wo_version` is required whenever " +
					"this one is set. Everything `user_data` says about the document itself applies here " +
					"unchanged: write it as plain text rather than `base64encode(...)`, and give the VPC " +
					"an outbound path if cloud-init reaches the network. One rule does NOT carry over: " +
					"unlike `user_data`, an empty document is rejected here — omit the attribute for no user " +
					"data. " +
					"Terraform cannot see a write-only value, so it cannot detect a change to this document " +
					"in either direction: changing `user_data_wo_version` is what makes the next apply send " +
					"the current document, and it does so by REPLACING the instance — matching `user_data`, " +
					"which is create-only for the same reason. Editing the document without touching the " +
					"version does nothing. `user_data_hash` is null on this path; there is no config value " +
					"in state to hash, and a digest of the document does not belong in state either.",
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
				// No RequiresReplace here: a write-only attribute is null in prior
				// state, plan and final state, so a plan modifier on it compares
				// null against null and can never fire. The version companion is
				// the attribute that carries the replacement.
				Validators: []validator.String{
					// Omit the attribute for "no user data". An empty document
					// here is always a mistake — an unset variable, a template
					// that rendered to nothing — and it is not null, so it
					// passes the null guard and reaches the wire, where
					// omitempty drops it. The result is a REPLACEMENT that
					// boots the new instance with no cloud-init at all, on a
					// green apply, with no plan line to show it: the
					// write-only path removes the diff the legacy attribute
					// would have displayed.
					stringvalidator.LengthAtLeast(1),
					stringvalidator.ConflictsWith(path.MatchRoot("user_data")),
					stringvalidator.AlsoRequires(path.MatchRoot("user_data_wo_version")),
				},
			},
			"user_data_wo_version": schema.StringAttribute{
				Description: "Change tracker for `user_data_wo`, required whenever that attribute is set. " +
					"Changing this value REPLACES the instance and launches the replacement with the " +
					"current `user_data_wo` — the same lifecycle a change to `user_data` has, since user " +
					"data is only ever read at first boot. Leaving it alone leaves the running instance " +
					"untouched however much the write-only document changes. Its content is arbitrary — a " +
					"counter or a date is typical — and unlike the document it is stored in state, so do " +
					"not derive it from the document or from anything in it: a digest of the document is " +
					"a digest of whatever the document carries, and it is printed verbatim in `terraform " +
					"plan` output. `terraform import` leaves this unset, so the first apply against an " +
					"imported instance plans a REPLACEMENT — set it to match what the instance was " +
					"launched with, or accept the rebuild.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.AlsoRequires(path.MatchRoot("user_data_wo")),
				},
			},
			"console_password": schema.StringAttribute{
				Description: "Password for the default OS user, usable only at the VNC console; SSH stays key-only. " +
					"Changing forces replacement — so this is not an attribute you can rotate in place; a new " +
					"value destroys and recreates the instance. " + docs.StateSecretNote,
				Optional:  true,
				Sensitive: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"instance_access": schema.BoolAttribute{
				Description: "Install the Frostmoln in-guest agent at first boot to enable `fm ssh` terminal and `fm forward` access to the instance (using a session also requires the tenant's `instance-access` entitlement). Create-time only; the API does not return it, and `terraform import` leaves it unset — a `true` config on an imported instance plans a replacement. Enabling or disabling the agent forces replacement; unset and `false` both mean no agent, and switching between them does not.",
				Optional:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplaceIf(
						instanceAccessRequiresReplace,
						instanceAccessReplaceDescription,
						instanceAccessReplaceDescription,
					),
				},
			},
			"user_data_hash": schema.StringAttribute{
				Description: "SHA256 hash of the user data, used for change detection. Computed from the " +
					"configured `user_data`, so it is null when the document is supplied through " +
					"`user_data_wo` — there is no config value in state to hash, and `user_data_wo_version` " +
					"is what carries change detection on that path.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the instance.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The current status of the instance.",
				Computed:    true,
			},
			"flavor_name": schema.StringAttribute{
				Description: "The name of the instance flavor.",
				Computed:    true,
			},
			"image_name": schema.StringAttribute{
				Description: "The name of the image used to create the instance.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the instance.",
				Computed:    true,
			},
			"public_ip": schema.StringAttribute{
				Description: "The public IP address of the instance, if assigned.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the instance was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// instanceAccessReplaceDescription documents when instance_access forces replacement.
const instanceAccessReplaceDescription = "Replacement is required when the agent is being enabled or disabled. Unset and `false` are equivalent (no agent), so switching between them does not force replacement."

// instanceAccessRequiresReplace requires replacement only on a real
// enable/disable transition. Agent enrollment happens via config-drive at first
// boot (ADR-0092), so changing the effective value needs a rebuild — but null
// and false both mean "no agent" and serialize identically on the wire
// (omitempty), so flipping between those spellings must not destroy the VM. An
// unknown plan value may resolve to true, so it conservatively replaces.
func instanceAccessRequiresReplace(_ context.Context, req planmodifier.BoolRequest, resp *boolplanmodifier.RequiresReplaceIfFuncResponse) {
	if req.PlanValue.IsUnknown() {
		resp.RequiresReplace = true
		return
	}
	resp.RequiresReplace = req.PlanValue.ValueBool() != req.StateValue.ValueBool()
}

func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan InstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A write-only attribute is null in the plan by construction; its value only
	// ever reaches the provider through the config.
	userDataWO := writeOnlyUserData(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !userDataWO.IsNull() {
		apiReq.UserData = userDataWO.ValueString()
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/instances"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create instance", err.Error())
		return
	}

	// Instance create routes through provisioning, which returns 202 + an Operation
	// envelope (operationId only, NOT the instance). Poll the operation to
	// completion (the workflow waits for the instance to reach running before
	// completing), then read by its resolved resourceId. A 201 with the instance
	// body is still accepted for a synchronous backend. Mirrors the volume +
	// snapshot + load_balancer resources.
	var instanceID string
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to parse operation response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout())
		if waitErr != nil {
			resp.Diagnostics.AddError("Instance failed to reach running state", waitErr.Error())
			return
		}
		instanceID = done.ResourceID
	} else {
		inst, parseErr := client.ParseResponse[apiInstance](apiResp)
		if parseErr != nil {
			resp.Diagnostics.AddError("Failed to parse instance response", parseErr.Error())
			return
		}
		instanceID = inst.ID
	}
	if instanceID == "" {
		resp.Diagnostics.AddError(
			"Instance Operation Returned No Resource ID",
			"The instance create operation completed but returned no resource ID. The instance may exist in the backend without being tracked in Terraform state - check `fm compute instance list` and import it if necessary.",
		)
		return
	}

	// Store user_data hash before fromAPI (which doesn't touch user_data fields).
	// It stays null on the write-only path: the hash exists to detect a change to
	// the configured document, and there is no configured document in state to
	// hash — user_data_wo_version does that job instead. Hashing the write-only
	// value would also put a digest of it in state, which is what the attribute
	// exists to avoid.
	if !plan.UserData.IsNull() && !plan.UserData.IsUnknown() {
		plan.UserDataHash = types.StringValue(computeUserDataHash(plan.UserData.ValueString()))
	} else {
		plan.UserDataHash = types.StringNull()
	}

	// Read the final state (the operation completion means the instance is running).
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+instanceID), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read instance after creation", err.Error())
		return
	}
	finalInst, err := client.ParseResponse[apiInstance](readResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, finalInst, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state InstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Preserve write-only fields before refreshing from API.
	savedUserData := state.UserData
	savedUserDataHash := state.UserDataHash
	savedInstanceAccess := state.InstanceAccess

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read instance", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse instance response", err.Error())
		return
	}

	state.fromAPI(ctx, inst, &resp.Diagnostics)

	// Restore write-only fields that the API doesn't return.
	state.UserData = savedUserData
	state.UserDataHash = savedUserDataHash
	state.InstanceAccess = savedInstanceAccess

	// Drift-detect security groups against the authoritative applied set. The
	// plain instance read returns Nova-aggregated SG NAMES (a different identifier
	// space than the UUIDs the user configures), so fromAPI only preserves the
	// configured set. GET /instances/{id}/security-groups returns the real Neutron
	// SG UUIDs: adopt them when every port shares one set (uniform) so out-of-band
	// changes (portal/CLI/console/another client) surface as drift. When the
	// per-port sets differ the union is lossy (a PUT would expand a subset port),
	// so preserve + warn instead. A failed subresource read must not break the
	// whole refresh — keep the preserved set.
	r.reconcileSecurityGroups(ctx, &state, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// getSecurityGroups fetches the authoritative applied security-group set from
// GET /instances/{id}/security-groups (Neutron SG UUIDs).
func (r *instanceResource) getSecurityGroups(ctx context.Context, id string) (*apiInstanceSecurityGroups, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+id+"/security-groups"), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiInstanceSecurityGroups](apiResp)
}

// reconcileSecurityGroups updates state.SecurityGroups from the authoritative
// subresource so out-of-band changes surface as drift. It fails soft: a read
// error or a non-uniform (lossy-union) instance leaves the preserved set intact.
func (r *instanceResource) reconcileSecurityGroups(ctx context.Context, state *InstanceModel, diags *diag.Diagnostics) {
	// Only drift-detect an attribute the user actually manages. security_groups is
	// Optional (not Computed): when config omits it, state is null while Neutron
	// still puts a non-empty SG set (the tenant default) on every port. Adopting
	// that set would make the next plan propose clearing it (Update -> clear) — a
	// destructive, security-relevant diff the user never asked for, and on provider
	// upgrade it would fire for every pre-existing instance with an unset
	// security_groups. Leave null null. (Capturing the applied set on import would
	// need Optional+Computed, a larger change — out of scope here.)
	if state.SecurityGroups.IsNull() {
		return
	}

	sg, err := r.getSecurityGroups(ctx, state.ID.ValueString())
	if err != nil {
		tflog.Warn(ctx, "could not read authoritative security groups; preserving configured set", map[string]any{
			"instance_id": state.ID.ValueString(),
			"error":       err.Error(),
		})
		return
	}

	if !sg.Uniform {
		diags.AddWarning(
			"Per-port security groups differ; drift not tracked",
			"This instance's ports do not all share the same security-group set, so Terraform cannot represent them as a single security_groups value. The configured set is preserved and drift is not detected. Edit security groups per port (portal/CLI), or set them uniformly across all ports.",
		)
		return
	}

	// Uniform + a managed (non-null) attr: the authoritative UUID set is the truth.
	// A non-null empty state vs an empty applied set stays stable ([] == []); an
	// out-of-band clear (authoritative empty) is adopted as real drift.
	sgSet, d := types.SetValueFrom(ctx, types.StringType, sg.SecurityGroupIDs)
	diags.Append(d...)
	if !d.HasError() {
		state.SecurityGroups = sgSet
	}
}

func (r *instanceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan InstanceModel
	var state InstanceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	// Preserve write-only fields. instance_access is deliberately NOT
	// overwritten from state: its plan modifier allows an in-place null<->false
	// respelling (both mean "no agent"), and the final state must match the plan
	// exactly. Any real change forces replacement and fromAPI never touches it,
	// so the plan value is already correct.
	plan.UserData = state.UserData
	plan.UserDataHash = state.UserDataHash

	// Check if flavor_id changed (resize workflow).
	if !plan.FlavorID.Equal(state.FlavorID) {
		if err := r.resizeInstance(ctx, id, plan.FlavorID.ValueString()); err != nil {
			resp.Diagnostics.AddError("Failed to resize instance", err.Error())
			return
		}
	}

	// Security groups change in place via the dedicated subresource (replace
	// semantics). The write is async; setSecurityGroups waits for the backend to
	// converge so a dependent resource / subsequent read sees the applied set.
	if !plan.SecurityGroups.Equal(state.SecurityGroups) {
		var sgIDs []string
		if !plan.SecurityGroups.IsNull() && !plan.SecurityGroups.IsUnknown() {
			resp.Diagnostics.Append(plan.SecurityGroups.ElementsAs(ctx, &sgIDs, false)...)
			if resp.Diagnostics.HasError() {
				return
			}
		}
		if err := r.setSecurityGroups(ctx, id, sgIDs); err != nil {
			resp.Diagnostics.AddError("Failed to update security groups", err.Error())
			return
		}
	}

	// name + tags are updatable in place via the compute update API.
	nameChanged := !plan.Name.Equal(state.Name)
	tagsChanged := !plan.Tags.Equal(state.Tags)

	if nameChanged || tagsChanged {
		updateReq := plan.toUpdateRequest(ctx, tagsChanged, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		_, err := r.client.Patch(ctx, r.client.TenantPath("/instances/"+id), updateReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to update instance", err.Error())
			return
		}
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read instance after update", err.Error())
		return
	}

	inst, err := client.ParseResponse[apiInstance](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse instance response", err.Error())
		return
	}

	plan.fromAPI(ctx, inst, &resp.Diagnostics)

	// Restore write-only fields (instance_access keeps the plan value — see above).
	plan.UserData = state.UserData
	plan.UserDataHash = state.UserDataHash

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// resizeInstance submits the flavor change and waits for it to complete.
//
// The whole stop/migrate/confirm dance belongs to the backend's Temporal resize
// workflow, which also returns the guest to the power state it was in. The
// provider previously drove it by hand — POST /stop, poll for "stopped", POST
// /resize, POST /start, poll for "running" — and that could never work: the
// three POSTs each start their OWN async workflow and were never awaited (so
// /start raced the resize that had not begun, and was refused: compute allows
// start only from SHUTOFF, never from RESIZE), and the poller compared against
// lowercase "stopped"/"running" while the API returns Nova's uppercase
// SHUTOFF/ACTIVE, so the very first wait ran out its budget on a VM that had
// already stopped. The reported symptom was exactly that: the VM stops and
// nothing further happens.
func (r *instanceResource) resizeInstance(ctx context.Context, id, newFlavorID string) error {
	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/instances/"+id+"/resize"), apiResizeInstanceRequest{FlavorID: newFlavorID})
	if err != nil {
		// A 409 RESIZE_IN_PROGRESS is not necessarily someone else's resize — it is
		// most often OUR OWN. The api-gateway retries a POST whose upstream write
		// timed out, the resize workflow id is deterministic, and the conflict policy
		// is FAIL, so a retried request is refused for the run its own first attempt
		// started. Provisioning carries that run's operation id in the refusal
		// precisely so a client can attach instead of failing an apply over a resize
		// that is running and will succeed.
		//
		// Attaching is safe even when the in-flight resize is NOT ours: it may target
		// a different flavor (which is why the policy is FAIL rather than
		// USE_EXISTING), and Update's read-back writes the observed flavor into
		// state — so a mismatch surfaces as an apply error rather than a green apply
		// claiming a size the VM does not have.
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code == resizeInProgressCode && apiErr.OperationID != "" {
			tflog.Info(ctx, "a resize of this instance is already running; attaching to it instead of failing", map[string]any{
				"instance_id":  id,
				"operation_id": apiErr.OperationID,
			})
			return r.awaitResizeOperation(ctx, apiErr.OperationID)
		}
		return err
	}
	// Resize has exactly one success shape: 202 + an Operation. Treating any other
	// 2xx as success is the one branch that would write the new flavor_id into state
	// having verified nothing at all.
	if !apiResp.IsAccepted() {
		return fmt.Errorf("unexpected status %d from instance resize; expected 202 Accepted", apiResp.StatusCode)
	}
	op, opErr := client.ParseResponse[client.Operation](apiResp)
	if opErr != nil {
		return fmt.Errorf("parse resize operation response: %w", opErr)
	}
	return r.awaitResizeOperation(ctx, op.OperationID)
}

// resizeInProgressCode is provisioning's refusal when a resize of this instance is
// already running (instance_handler.go ResizeInstance).
const resizeInProgressCode = "RESIZE_IN_PROGRESS"

// awaitResizeOperation waits for a resize operation to reach a terminal state.
func (r *instanceResource) awaitResizeOperation(ctx context.Context, operationID string) error {
	// An empty id would poll .../operations/ — a 404 the poller retries to the
	// deadline, reporting a bare timeout for a resize that may well have started.
	if operationID == "" {
		return fmt.Errorf("instance resize was accepted but returned no operation id; the resize may be running — check `fm compute instance show` before retrying")
	}
	if _, err := r.client.WaitForOperation(ctx, operationID, r.getPollInterval(), r.getResizeTimeout()); err != nil {
		return fmt.Errorf("resize did not complete: %w", err)
	}
	return nil
}

// setSecurityGroups REPLACES the instance's security groups in place via
// PUT /instances/{id}/security-groups (replace semantics, Neutron SG UUIDs).
// An empty sgIDs clears all SGs (clear flag set so the backend doesn't reject it
// as a probable dropped field). The PUT routes through provisioning and returns
// 202 + an Operation; we wait for it to complete so the applied set is visible to
// a subsequent read / dependent resource (the change lands asynchronously — do
// not race the read).
func (r *instanceResource) setSecurityGroups(ctx context.Context, id string, sgIDs []string) error {
	body := apiSetInstanceSecurityGroupsRequest{
		SecurityGroupIDs:    sgIDs,
		ClearSecurityGroups: len(sgIDs) == 0,
	}
	apiResp, err := r.client.Put(ctx, r.client.TenantPath("/instances/"+id+"/security-groups"), body)
	if err != nil {
		return err
	}
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			return fmt.Errorf("parse security-group operation response: %w", opErr)
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, r.getPollInterval(), r.getPollTimeout()); waitErr != nil {
			return fmt.Errorf("security-group update did not complete: %w", waitErr)
		}
	}
	return nil
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state InstanceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	_, err := r.client.Delete(ctx, r.client.TenantPath("/instances/"+id))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete instance", err.Error())
		return
	}

	// Wait for the instance to be fully deleted (404 on GET).
	_, err = client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{"deleted"},
		// No ErrorStates. This read "error", which could never match compute's
		// uppercase ERROR — the same vocabulary bug the resize path had. Correcting
		// the case would be a REGRESSION, not a fix: destroying an instance that is
		// already in ERROR is the most ordinary thing anyone does with a broken VM,
		// and making ERROR terminal here would fail that destroy instead of waiting
		// for the 404 that is the real terminal signal.
		ResourceName: "instance",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/instances/"+id), nil)
			if pollErr != nil {
				if client.IsNotFound(pollErr) {
					return "deleted", nil
				}
				return "", pollErr
			}
			inst, parseErr := client.ParseResponse[apiInstance](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			return inst.Status, nil
		},
	})
	if err != nil {
		resp.Diagnostics.AddError("Instance failed to delete", err.Error())
	}
}

// writeOnlyUserData reads user_data_wo out of the configuration. It is read
// straight from the config rather than through InstanceModel because a
// write-only attribute is null everywhere else — prior state, plan and final
// state. The config still carries the value at this point: the framework nulls
// write-only attributes in the planned and final state, never in the config,
// which is also what lets the attribute validators see it.
//
// An unknown value fails CLOSED. Config is fully resolved by apply, so this
// should be unreachable — but the alternative spelling (adding !IsUnknown() to
// the caller's guard) would drop the document, launch a VM with no cloud-init
// and report success, which is the one outcome this resource must never
// produce silently.
func writeOnlyUserData(ctx context.Context, config tfsdk.Config, diags *diag.Diagnostics) types.String {
	var v types.String
	diags.Append(config.GetAttribute(ctx, path.Root("user_data_wo"), &v)...)
	if v.IsUnknown() {
		diags.AddError(
			"user_data_wo Is Unknown At Apply",
			"The write-only user_data_wo value was still unknown when the instance was created, so the "+
				"document could not be sent. The instance was NOT created. This is a bug in the provider "+
				"or in Terraform — please report it.",
		)
	}
	return v
}

func (r *instanceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
