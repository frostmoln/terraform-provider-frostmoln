package vpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &vpcResource{}
	_ resource.ResourceWithImportState = &vpcResource{}
)

type vpcResource struct {
	client *client.Client
}

// NewResource returns a new VPC resource.
func NewResource() resource.Resource {
	return &vpcResource{}
}

func (r *vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (r *vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC in the NordicLight Cloud Platform.",
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
			"region": schema.StringAttribute{
				Description: "The region for the VPC.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
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

	var vpc apiVPC
	if err := json.Unmarshal(apiResp.Body, &vpc); err != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Response", err.Error())
		return
	}

	// If 202 Accepted, poll until active
	if apiResp.StatusCode == http.StatusAccepted {
		_, err := client.WaitForState(ctx, client.PollConfig{
			Interval:     2 * time.Second,
			Timeout:      5 * time.Minute,
			TargetStates: []string{"active"},
			ErrorStates:  []string{"error"},
			ResourceName: "VPC",
			PollFunc: func(ctx context.Context) (string, error) {
				pollResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", vpc.ID)), nil)
				if err != nil {
					return "", err
				}
				var polledVPC apiVPC
				if err := json.Unmarshal(pollResp.Body, &polledVPC); err != nil {
					return "", err
				}
				vpc = polledVPC
				return polledVPC.Status, nil
			},
		})
		if err != nil {
			resp.Diagnostics.AddError("VPC Creation Failed", err.Error())
			return
		}
	}

	plan.fromAPI(ctx, &vpc, &resp.Diagnostics)
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

	apiResp, err := r.client.Patch(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", state.ID.ValueString())), updateReq)
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

	query := url.Values{}
	query.Set("force", "true")

	_, err := r.client.DeleteWithQuery(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", state.ID.ValueString())), query)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete VPC", err.Error())
		return
	}
}

func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
