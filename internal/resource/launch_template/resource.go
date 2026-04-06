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
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
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
				Description: "User data to provide to instances at launch. This is write-only; the API does not return it.",
				Optional:    true,
				Sensitive:   true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
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

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Preserve user_data before fromAPI (which doesn't touch it).
	savedUserData := plan.UserData

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

	// Restore write-only field.
	plan.UserData = savedUserData

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *launchTemplateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state LaunchTemplateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Preserve write-only fields before refreshing from API.
	savedUserData := state.UserData

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

	state.fromAPI(ctx, lt, &resp.Diagnostics)

	// Restore write-only fields.
	state.UserData = savedUserData

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

	id := state.ID.ValueString()

	// Save write-only field from the plan before fromAPI overwrites model fields.
	savedUserData := plan.UserData

	updateReq := plan.toUpdateRequest(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
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

	// Restore write-only field that the API does not return.
	plan.UserData = savedUserData

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
