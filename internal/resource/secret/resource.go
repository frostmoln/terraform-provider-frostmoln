package secret

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &secretResource{}
	_ resource.ResourceWithImportState = &secretResource{}
)

// NewResource returns a new secret resource factory.
func NewResource() resource.Resource {
	return &secretResource{}
}

type secretResource struct {
	client *client.Client
}

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a secret in the Frostmoln platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the secret.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the secret. Cannot be changed after creation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"description": schema.StringAttribute{
				Description: "A description of the secret.",
				Optional:    true,
			},
			"secret_value": schema.StringAttribute{
				Description: "The secret value. This is sensitive and will not be shown in plan output.",
				Required:    true,
				Sensitive:   true,
			},
			"content_type": schema.StringAttribute{
				Description: "The content type of the secret value. Defaults to \"text/plain\".",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("text/plain"),
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the secret.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"max_versions": schema.Int64Attribute{
				Description: "The maximum number of versions to retain. Defaults to 10.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(10),
			},
			"recovery_window_days": schema.Int64Attribute{
				Description: "The number of days to retain a deleted secret before permanent removal. Defaults to 7.",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(7),
			},
			"current_version": schema.Int64Attribute{
				Description: "The current version number of the secret.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the secret.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the secret was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the secret was last updated.",
				Computed:    true,
			},
		},
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/secrets"), apiReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create secret", err.Error())
		return
	}

	s, err := client.ParseResponse[apiSecret](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse secret response", err.Error())
		return
	}

	plan.fromAPI(ctx, s, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/secrets/"+state.ID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read secret", err.Error())
		return
	}

	s, err := client.ParseResponse[apiSecret](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse secret response", err.Error())
		return
	}

	state.fromAPI(ctx, s, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SecretModel
	var state SecretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	updateReq := plan.toUpdateRequest(ctx, &state, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Put(ctx, r.client.TenantPath("/secrets/"+id), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update secret", err.Error())
		return
	}

	// Refresh state from API.
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/secrets/"+id), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read secret after update", err.Error())
		return
	}

	s, err := client.ParseResponse[apiSecret](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse secret response", err.Error())
		return
	}

	plan.fromAPI(ctx, s, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath("/secrets/"+state.ID.ValueString()))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete secret", err.Error())
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
