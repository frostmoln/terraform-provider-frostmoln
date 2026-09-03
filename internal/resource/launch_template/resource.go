package launch_template

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/writeonly"
)

var (
	_ resource.Resource                = &launchTemplateResource{}
	_ resource.ResourceWithImportState = &launchTemplateResource{}
)

// NewResource returns a new launch template resource factory.
func NewResource() resource.Resource {
	return &launchTemplateResource{}
}

type launchTemplateResource struct {
	client *client.Client
}

func (r *launchTemplateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_launch_template"
}

func (r *launchTemplateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a launch template for compute instances in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the launch template.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the launch template.",
				Required:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID for instances launched from this template.",
				Required:    true,
			},
			"image_id": schema.StringAttribute{
				Description: "The image ID for instances launched from this template.",
				Required:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID for instances launched from this template.",
				Required:    true,
			},
			"ssh_key_ids": schema.SetAttribute{
				Description: "The SSH key IDs to inject into instances launched from this template.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
			},
			"security_group_ids": schema.SetAttribute{
				Description: "The security group IDs to attach to instances launched from this template.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.UseStateForUnknown(),
				},
			},
			"user_data": schema.StringAttribute{
				Description: "User data to provide to instances at launch — typically a cloud-init " +
					"document. The platform returns the stored document on every read, but the provider " +
					"does not decode it, so the value you configure is preserved from state on refresh and a " +
					"change made outside Terraform is not detected. " +
					docs.UserDataStateNote + " Prefer `user_data_wo`, which carries the same document but is " +
					"never written to state; the two are mutually exclusive.\n\n" +
					"**Write the document as plain text — `file(\"cloud-init.yaml\")`, not " +
					"`base64encode(file(...))`.** Base64 is accepted by the API, but it must NOT be " +
					"used when instances launched from this template also get SSH keys, a console " +
					"password or `instance_access`. In those cases the platform merges its own " +
					"cloud-config into the document, and the merge dispatches on the literal " +
					"`#cloud-config` prefix: a base64 blob does not carry it, so the blob is treated " +
					"as a shell script and combined alongside the platform's cloud-config instead of " +
					"into it. The launch succeeds and nothing surfaces in Terraform, but the document " +
					"never runs as cloud-config. Plain text is correct in both directions: a " +
					"`#cloud-config` document is merged in place, and a `#!` script is combined as " +
					"intended. See the example below.\n\n" +
					"Changing this updates the template in place; instances already launched from it " +
					"keep the user data they were created with. A cloud-init step that installs " +
					"packages or calls an external endpoint needs the launched instance's VPC to have " +
					"an outbound path — declare a `frostmoln_gateway` for the VPC, or the step fails " +
					"on first boot with no internet and no name resolution.",
				Optional:  true,
				Sensitive: true,
				Validators: []validator.String{
					stringvalidator.PreferWriteOnlyAttribute(path.MatchRoot("user_data_wo")),
				},
			},
			"user_data_wo": schema.StringAttribute{
				Description: "User data to provide to instances at launch, as a [write-only argument]" +
					"(https://developer.hashicorp.com/terraform/language/resources/ephemeral/write-only): the " +
					"document reaches the provider on apply and is never written to the plan or to state, so " +
					"anything embedded in it stays out of the state file. Requires Terraform 1.11 or later. " +
					"Mutually exclusive with `user_data`, and `user_data_wo_version` is required whenever this " +
					"one is set. Everything `user_data` says about the document itself applies here unchanged: " +
					"write it as plain text rather than `base64encode(...)`, and give the launched instance's " +
					"VPC an outbound path if cloud-init reaches the network. One rule does NOT carry over: " +
					"unlike `user_data`, an empty document is rejected here — omit the attribute for no user " +
					"data. Terraform cannot see a write-only " +
					"value, so it cannot detect a change to this document in either direction: changing " +
					"`user_data_wo_version` is what makes the next apply send the current document, and it " +
					"updates the template in place. Editing the document without touching the version does " +
					"nothing.",
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
				// No plan modifiers: a write-only attribute is null in prior state,
				// the plan and the final state, so one here would compare null
				// against null and could never fire. The version companion carries
				// the change signal.
				Validators: []validator.String{
					// Omit the attribute for "no user data". An empty value here
					// is always a mistake — an unset variable, a template that
					// rendered to nothing — and on the write-only path it clears
					// the stored document with no plan line to show it, unlike
					// the legacy attribute where "" is at least a visible diff.
					stringvalidator.LengthAtLeast(1),
					stringvalidator.ConflictsWith(path.MatchRoot("user_data")),
					stringvalidator.AlsoRequires(path.MatchRoot("user_data_wo_version")),
				},
			},
			"user_data_wo_version": schema.StringAttribute{
				Description: "Change tracker for `user_data_wo`, required whenever that attribute is set. " +
					"Any change to this value makes the next apply send the current `user_data_wo` to the " +
					"platform, updating the template in place; leaving it alone leaves the stored document " +
					"untouched however much the write-only value changes. Instances already launched from the " +
					"template keep the user data they were created with either way. Its content is arbitrary " +
					"— a counter or a date is typical — and unlike the document it is stored in state, so do " +
					"not derive it from the document or from anything in it: a digest of the document is a " +
					"digest of whatever the document carries, and it is printed verbatim in `terraform plan` " +
					"output. `terraform import` leaves this unset, so the first apply against an imported " +
					"template sends the configured document again as a normal in-place update.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.AlsoRequires(path.MatchRoot("user_data_wo")),
				},
			},
			"metadata": schema.MapAttribute{
				Description: "Key-value metadata for the launch template.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Key-value tags for the launch template.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the launch template was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the launch template was last updated.",
				Computed:    true,
			},
		},
	}
}

func (r *launchTemplateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *launchTemplateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LaunchTemplateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A write-only attribute is null in the plan by construction; its value only
	// ever reaches the provider through the config.
	userDataWO := userDataWO.Read(ctx, req.Config, &resp.Diagnostics)
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

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/launch-templates"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create launch template", err.Error())
		return
	}

	lt, err := client.ParseResponse[apiLaunchTemplate](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse launch template response", err.Error())
		return
	}

	plan.fromAPI(ctx, lt, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *launchTemplateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LaunchTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/launch-templates/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read launch template", err.Error())
		return
	}

	lt, err := client.ParseResponse[apiLaunchTemplate](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse launch template response", err.Error())
		return
	}

	// fromAPI leaves both user_data and the write-only pair alone, so the
	// configured document and the version companion carry over from prior state
	// untouched. See the note at the end of fromAPI for why the response's
	// document is not decoded at all.
	state.fromAPI(ctx, lt, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *launchTemplateResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan LaunchTemplateModel
	var state LaunchTemplateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	userDataWO := userDataWO.Read(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	updateReq := plan.toUpdateRequest(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	// user_data_wo is null on both sides of the diff, so toUpdateRequest can
	// never see it change. The practitioner-set version companion is the only
	// signal that a new document should be sent.
	if !userDataWO.IsNull() && !plan.UserDataWOVer.Equal(state.UserDataWOVer) {
		v := userDataWO.ValueString()
		updateReq.UserData = &v
	}

	_, err := r.client.Patch(ctx, r.client.TenantPath("/launch-templates/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update launch template", err.Error())
		return
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/launch-templates/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read launch template after update", err.Error())
		return
	}

	lt, err := client.ParseResponse[apiLaunchTemplate](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse launch template response", err.Error())
		return
	}

	plan.fromAPI(ctx, lt, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *launchTemplateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state LaunchTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/launch-templates/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete launch template", err.Error())
	}
}

func (r *launchTemplateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// userDataWO is the write-only triple for the template's cloud-init document.
//
// Not ExactlyOne: user_data was already Optional before the write-only form
// existed, so a template with no user data at all stays valid. See the package
// comment on internal/writeonly.
var userDataWO = writeonly.Attr{
	WO:      "user_data_wo",
	Version: "user_data_wo_version",
	Legacy:  "user_data",
	Subject: "the launch template",
}
