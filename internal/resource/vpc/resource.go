package vpc

import (
	"context"
	"encoding/json"
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
			"interact.",
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

	// VPC create routes through provisioning → 202 + an Operation envelope
	// (operationId only, NOT the VPC). Poll the operation, then read by its
	// resolved resourceId. A non-202 body is parsed directly for a sync backend.
	// (Previously json.Unmarshal'd the envelope into apiVPC → empty ID → polled
	// /vpcs/{empty} against the real backend.)
	var vpc apiVPC
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute)
		if waitErr != nil {
			resp.Diagnostics.AddError("VPC Creation Failed", waitErr.Error())
			return
		}
		if done.ResourceID == "" {
			resp.Diagnostics.AddError("VPC Operation Returned No Resource ID",
				"The VPC create operation completed but returned no resource ID. Check `fm network vpc list` and import it if necessary.")
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
