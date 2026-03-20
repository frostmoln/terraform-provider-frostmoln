package ssh_key

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &sshKeyResource{}
	_ resource.ResourceWithImportState = &sshKeyResource{}
)

// NewResource returns a new SSH key resource factory.
func NewResource() resource.Resource {
	return &sshKeyResource{}
}

type sshKeyResource struct {
	client *client.Client
}

func (r *sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages an SSH key in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the SSH key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the SSH key.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"public_key": schema.StringAttribute{
				Description: "The public key content.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"fingerprint": schema.StringAttribute{
				Description: "The fingerprint of the SSH key.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the SSH key was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *sshKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SSHKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest()
	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/sshkeys"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create SSH key", err.Error())
		return
	}

	key, err := client.ParseResponse[apiSSHKey](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse SSH key response", err.Error())
		return
	}

	plan.fromAPI(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SSHKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/sshkeys/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read SSH key", err.Error())
		return
	}

	key, err := client.ParseResponse[apiSSHKey](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse SSH key response", err.Error())
		return
	}

	state.fromAPI(key)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sshKeyResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"SSH keys cannot be updated. All attribute changes require resource replacement.",
	)
}

func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SSHKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/sshkeys/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete SSH key", err.Error())
	}
}

func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
