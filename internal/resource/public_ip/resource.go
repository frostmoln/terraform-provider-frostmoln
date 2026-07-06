package public_ip

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

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
	_ resource.Resource                = &publicIPResource{}
	_ resource.ResourceWithImportState = &publicIPResource{}
)

type publicIPResource struct {
	client *client.Client
}

// NewResource returns a new public IP resource.
func NewResource() resource.Resource {
	return &publicIPResource{}
}

func (r *publicIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip"
}

func (r *publicIPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a public IP in the Frostmoln Cloud Platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the public IP.",
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
			"instance_id": schema.StringAttribute{
				Description: "The ID of the instance to associate with. Set to associate, remove to disassociate.",
				Optional:    true,
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the public IP.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the public IP.",
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

func (r *publicIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// resolvePortID looks up the Neutron port to attach a public IP to by reading
// the instance and returning its first network port. The provisioning associate
// endpoint requires a portId; instance_id is the user-friendly input the
// provider resolves on their behalf (mirrors how the portal associates by port).
func (r *publicIPResource) resolvePortID(ctx context.Context, instanceID string) (string, error) {
	readResp, err := r.client.Get(ctx, r.client.TenantPath("/instances/"+instanceID), nil)
	if err != nil {
		return "", fmt.Errorf("failed to read instance %s: %w", instanceID, err)
	}
	var inst apiInstanceForPort
	if err := json.Unmarshal(readResp.Body, &inst); err != nil {
		return "", fmt.Errorf("failed to parse instance %s response: %w", instanceID, err)
	}
	for _, n := range inst.Networks {
		if n.PortID != "" {
			return n.PortID, nil
		}
	}
	return "", fmt.Errorf("instance %s has no network port available to associate the public IP with", instanceID)
}

func (r *publicIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PublicIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	allocateReq := plan.toAllocateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/public-ips"), allocateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Allocate Public IP", err.Error())
		return
	}

	// Allocate routes through provisioning → 202 + an Operation envelope
	// (operationId only, NOT the public IP). Resolve the FIP id from the
	// operation; a non-202 body is parsed directly for a sync backend.
	var fip apiPublicIP
	var fipID string
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute)
		if waitErr != nil {
			resp.Diagnostics.AddError("Public IP Allocation Failed", waitErr.Error())
			return
		}
		fipID = done.ResourceID
		if fipID == "" {
			resp.Diagnostics.AddError("Public IP Operation Returned No Resource ID",
				"The public IP allocate operation completed but returned no resource ID. Check `fm network public-ip list` and import it if necessary.")
			return
		}
	} else {
		if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
			resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
			return
		}
		fipID = fip.ID
	}

	// If instance_id is set, associate after allocation (also async via provisioning).
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() {
		portID, portErr := r.resolvePortID(ctx, plan.InstanceID.ValueString())
		if portErr != nil {
			resp.Diagnostics.AddError("Failed to Resolve Instance Port", portErr.Error())
			return
		}
		assocReq := apiAssociatePublicIPRequest{PortID: portID}
		assocResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s/associate", fipID)), assocReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Associate Public IP", err.Error())
			return
		}
		if assocResp.IsAccepted() {
			op, opErr := client.ParseResponse[client.Operation](assocResp)
			if opErr != nil {
				resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
				return
			}
			if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
				resp.Diagnostics.AddError("Public IP Association Failed", waitErr.Error())
				return
			}
		}
	}

	// Read the final state by the resolved id (covers allocate-only and the
	// post-association state).
	readResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", fipID)), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Public IP After Creation", err.Error())
		return
	}
	if err := json.Unmarshal(readResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *publicIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PublicIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Public IP", err.Error())
		return
	}

	var fip apiPublicIP
	if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
		return
	}

	state.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *publicIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PublicIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state PublicIPModel
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
			disResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s/disassociate", fipID)), nil)
			if err != nil {
				resp.Diagnostics.AddError("Failed to Disassociate Public IP", err.Error())
				return
			}
			if disResp.IsAccepted() {
				op, opErr := client.ParseResponse[client.Operation](disResp)
				if opErr != nil {
					resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
					return
				}
				if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
					resp.Diagnostics.AddError("Public IP Disassociation Failed", waitErr.Error())
					return
				}
			}
		}

		// Associate if new instance_id is set
		if newInstanceID != "" {
			portID, portErr := r.resolvePortID(ctx, newInstanceID)
			if portErr != nil {
				resp.Diagnostics.AddError("Failed to Resolve Instance Port", portErr.Error())
				return
			}
			assocResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s/associate", fipID)), apiAssociatePublicIPRequest{PortID: portID})
			if err != nil {
				resp.Diagnostics.AddError("Failed to Associate Public IP", err.Error())
				return
			}
			if assocResp.IsAccepted() {
				op, opErr := client.ParseResponse[client.Operation](assocResp)
				if opErr != nil {
					resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
					return
				}
				if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
					resp.Diagnostics.AddError("Public IP Association Failed", waitErr.Error())
					return
				}
			}
		}
	}

	// Handle tags update
	if !plan.Tags.Equal(state.Tags) {
		updateReq := apiUpdatePublicIPRequest{}
		if !plan.Tags.IsNull() && !plan.Tags.IsUnknown() {
			tags := make(map[string]string)
			resp.Diagnostics.Append(plan.Tags.ElementsAs(ctx, &tags, false)...)
			if resp.Diagnostics.HasError() {
				return
			}
			updateReq.Tags = tags
		}
		_, err := r.client.Patch(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", fipID)), updateReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Update Public IP Tags", err.Error())
			return
		}
	}

	// Re-read the public IP to get the final state
	readResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", fipID)), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Public IP", err.Error())
		return
	}

	var fip apiPublicIP
	if err := json.Unmarshal(readResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *publicIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PublicIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Public IP", err.Error())
		return
	}
}

func (r *publicIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
