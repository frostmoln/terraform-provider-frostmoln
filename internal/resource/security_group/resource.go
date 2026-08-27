package security_group

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &securityGroupResource{}
	_ resource.ResourceWithImportState = &securityGroupResource{}
)

type securityGroupResource struct {
	client *client.Client
}

// NewResource returns a new security group resource.
func NewResource() resource.Resource {
	return &securityGroupResource{}
}

func (r *securityGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func (r *securityGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a security group in the Frostmoln Cloud Platform.\n\n" +
			"**This resource manages the GROUP ONLY, never its rules.** Rules are separate " +
			"`frostmoln_security_group_rule` resources — the same shape the AWS provider uses. " +
			"Nothing in this resource describes the rules the group carries: there is no `rules` " +
			"attribute and Read does not fetch any, so a `terraform plan` on this resource NEVER " +
			"reports rule drift, whatever the group's rules have become.\n\n" +
			"**A rule added out of band is invisible to Terraform.** A rule created through the " +
			"portal, the `fm` CLI or the API — by a colleague, a script, or an incident fix — " +
			"appears in no plan, is never flagged, and is never removed by `terraform apply` or " +
			"`terraform destroy`. The reverse case IS caught: a `frostmoln_security_group_rule` " +
			"this configuration owns that is deleted out of band is detected on refresh and " +
			"planned for re-creation. Reconciling added rules means listing the group outside " +
			"Terraform — `fm`, the portal and the API all return its full rule set — and then " +
			"importing each rule that should be managed with `terraform import " +
			"frostmoln_security_group_rule.<name> <security_group_id>/<rule_id>`.\n\n" +
			"**Every new security group starts with two allow-all egress rules that Terraform does " +
			"not manage.** Frostmoln adds no default rules of its own to a group created here, but " +
			"the underlying network service unconditionally creates one \"any protocol to " +
			"everywhere\" egress rule per address family — IPv4 and IPv6 — on every security group, " +
			"each carrying an EMPTY remote prefix. They are live and permissive from the moment the " +
			"group exists, they are returned by the API (so `fm`, the portal and the API all show " +
			"them), and this provider does not manage them: they appear in no plan and survive a " +
			"`terraform destroy` of every rule this configuration declares. Adding egress rules of " +
			"your own does not narrow them either — security group rules are additive, so traffic " +
			"matching any rule is allowed. A group that must not egress freely has to have those " +
			"two rules removed deliberately: either delete them outside Terraform, or import each " +
			"as a `frostmoln_security_group_rule` ONLY IN ORDER TO DESTROY IT, and remove the " +
			"block from configuration again once the destroy has run. Leaving the block in place " +
			"makes the next apply try to RE-CREATE the rule, and the platform refuses a rule with " +
			"no remote. `frostmoln_security_group_rule` also has no `ether_type` attribute, so the " +
			"IPv4 and IPv6 defaults are indistinguishable in configuration — only one of the two " +
			"could ever be expressed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the security group.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the security group.",
				Required:    true,
			},
			"description": schema.StringAttribute{
				Description: "A description of the security group.",
				Optional:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The ID of the VPC this security group belongs to.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the security group.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"is_default": schema.BoolAttribute{
				Description: "Whether this is the default security group.",
				Computed:    true,
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.UseStateForUnknown(),
				},
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

func (r *securityGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *securityGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SecurityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/security-groups"), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Security Group", err.Error())
		return
	}

	// Security-group create routes through provisioning → 202 + an Operation
	// envelope (operationId only). Poll the operation, then read by its resolved
	// resourceId. A non-202 body is parsed directly for a sync backend.
	var sg apiSecurityGroup
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute)
		if waitErr != nil {
			resp.Diagnostics.AddError("Security Group Creation Failed", waitErr.Error())
			return
		}
		if done.ResourceID == "" {
			resp.Diagnostics.AddError("Security Group Operation Returned No Resource ID",
				"The security group create operation completed but returned no resource ID. Check `fm network security-group list` and import it if necessary.")
			return
		}
		readResp, readErr := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s", done.ResourceID)), nil)
		if readErr != nil {
			resp.Diagnostics.AddError("Failed to Read Security Group After Creation", readErr.Error())
			return
		}
		if err := json.Unmarshal(readResp.Body, &sg); err != nil {
			resp.Diagnostics.AddError("Failed to Parse Security Group Response", err.Error())
			return
		}
	} else if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Security Group Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &sg, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securityGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SecurityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s", state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Security Group", err.Error())
		return
	}

	var sg apiSecurityGroup
	if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Security Group Response", err.Error())
		return
	}

	state.fromAPI(ctx, &sg, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *securityGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan SecurityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state SecurityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateReq := plan.toUpdateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// PUT, not PATCH — network registers PUT for the in-place update and
	// nothing registers PATCH. See the frostmoln_vpc Update for the full note.
	apiResp, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s", state.ID.ValueString())), updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Update Security Group", err.Error())
		return
	}

	var sg apiSecurityGroup
	if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Security Group Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &sg, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *securityGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SecurityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s", state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to Delete Security Group", err.Error())
		return
	}
}

func (r *securityGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
