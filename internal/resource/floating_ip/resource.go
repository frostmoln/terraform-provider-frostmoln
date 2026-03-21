package floating_ip

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &floatingIPResource{}
	_ resource.ResourceWithImportState = &floatingIPResource{}
)

type floatingIPResource struct {
	client *client.Client
}

// NewResource returns a new floating IP resource.
func NewResource() resource.Resource {
	return &floatingIPResource{}
}

func (r *floatingIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_floating_ip"
}

func (r *floatingIPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a floating IP in the Frostmoln Cloud Platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the floating IP.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"address": schema.StringAttribute{
				Description: "The allocated IP address.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"region": schema.StringAttribute{
				Description: "The region for the floating IP.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the instance to associate with. Set to associate, remove to disassociate.",
				Optional:    true,
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the floating IP.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the floating IP.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP of the associated instance.",
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
	}
}

func (r *floatingIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *floatingIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan FloatingIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	allocateReq := plan.toAllocateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/floating-ips"), allocateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Allocate Floating IP", err.Error())
		return
	}

	var fip apiFloatingIP
	if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Floating IP Response", err.Error())
		return
	}

	// If instance_id is set, associate after allocation
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() {
		assocReq := apiAssociateFloatingIPRequest{
			InstanceID: plan.InstanceID.ValueString(),
		}
		assocResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s/associate", fip.ID)), assocReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Associate Floating IP", err.Error())
			return
		}
		// Re-read the floating IP to get the updated state
		if err := json.Unmarshal(assocResp.Body, &fip); err != nil {
			// If the response doesn't contain the full FIP, fetch it
			readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s", fip.ID)), nil)
			if readErr != nil {
				resp.Diagnostics.AddError("Failed to Read Floating IP After Association", readErr.Error())
				return
			}
			if err := json.Unmarshal(readResp.Body, &fip); err != nil {
				resp.Diagnostics.AddError("Failed to Parse Floating IP Response", err.Error())
				return
			}
		}
	}

	plan.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *floatingIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state FloatingIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s", state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Floating IP", err.Error())
		return
	}

	var fip apiFloatingIP
	if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Floating IP Response", err.Error())
		return
	}

	state.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *floatingIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan FloatingIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state FloatingIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fipID := state.ID.ValueString()

	// Handle instance association/disassociation changes
	oldInstanceID := state.InstanceID.ValueString()
	newInstanceID := ""
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() {
		newInstanceID = plan.InstanceID.ValueString()
	}

	if oldInstanceID != newInstanceID {
		// Disassociate if previously associated
		if oldInstanceID != "" {
			_, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s/disassociate", fipID)), nil)
			if err != nil {
				resp.Diagnostics.AddError("Failed to Disassociate Floating IP", err.Error())
				return
			}
		}

		// Associate if new instance_id is set
		if newInstanceID != "" {
			assocReq := apiAssociateFloatingIPRequest{
				InstanceID: newInstanceID,
			}
			_, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s/associate", fipID)), assocReq)
			if err != nil {
				resp.Diagnostics.AddError("Failed to Associate Floating IP", err.Error())
				return
			}
		}
	}

	// Handle tags update
	if !plan.Tags.Equal(state.Tags) {
		updateReq := apiUpdateFloatingIPRequest{}
		if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
			tags := make(map[string]string)
			resp.Diagnostics.Append(plan.Tags.ElementsAs(ctx, &tags, false)...)
			if resp.Diagnostics.HasError() {
				return
			}
			updateReq.Tags = tags
		}
		_, err := r.client.Patch(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s", fipID)), updateReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Update Floating IP Tags", err.Error())
			return
		}
	}

	// Re-read the floating IP to get the final state
	readResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s", fipID)), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Floating IP", err.Error())
		return
	}

	var fip apiFloatingIP
	if err := json.Unmarshal(readResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Floating IP Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *floatingIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state FloatingIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/floating-ips/%s", state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Floating IP", err.Error())
		return
	}
}

func (r *floatingIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
