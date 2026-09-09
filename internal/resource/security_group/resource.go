package security_group

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
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
	_ resource.ResourceWithModifyPlan  = &securityGroupResource{}
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
			"**Every new security group starts with two allow-all egress rules.** Frostmoln adds no " +
			"default rules of its own to a group created here, but the underlying network service " +
			"unconditionally creates one \"any protocol to everywhere\" egress rule per address " +
			"family — IPv4 and IPv6 — on every security group, each carrying an EMPTY remote " +
			"prefix. They are live and permissive from the moment the group exists, they are " +
			"returned by the API (so `fm`, the portal and the API all show them), and adding egress " +
			"rules of your own does not narrow them — security group rules are additive, so traffic " +
			"matching any rule is allowed. Setting `delete_default_egress = true` removes both as " +
			"part of creating the group. Without it they are unmanaged: they appear in no plan and " +
			"survive a `terraform destroy` of every rule this configuration declares, and a group " +
			"that must not egress freely has to have them removed deliberately — either delete " +
			"them outside Terraform, or import each as a `frostmoln_security_group_rule` ONLY IN " +
			"ORDER TO DESTROY IT, and remove the block from configuration again once the destroy " +
			"has run. Leaving the block in place makes the next apply try to RE-CREATE the rule, " +
			"and the platform refuses a rule with no remote. `frostmoln_security_group_rule` also " +
			"has no `ether_type` attribute, so the IPv4 and IPv6 defaults are indistinguishable in " +
			"configuration — only one of the two could ever be expressed." +
			"\n\n**A security group Frostmoln provisioned for a managed service cannot be managed " +
			"here.** Groups created for a managed database, cache, webserver, messaging instance, " +
			"Kubernetes cluster or Application Gateway are visible in your account and returned by " +
			"the API, so they can be imported \u2014 but every write against one is refused with " +
			"`409` / `resource_in_use`, permanently. Reads still work, so an imported group " +
			"that matches its configuration plans empty and applies clean; what fails, every " +
			"time, is `terraform destroy` and any `apply` that plans a change to it. There is " +
			"no attribute that predicts this and no force flag, so the only exit is " +
			"`terraform state rm`. Do not import one.",
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
			"delete_default_egress": schema.BoolAttribute{
				Description: "Delete the two allow-all egress rules the network service injects into " +
					"every new group (one per address family, each with an EMPTY remote prefix) as part " +
					"of creating it. Those defaults allow all outbound traffic, and declaring egress " +
					"rules of your own does not narrow them — rules are additive. When this is true, " +
					"Create deletes both right after the group exists, matched on direction and empty " +
					"remote prefix, never on address family — a rule has no `ether_type`, so the IPv4 " +
					"and IPv6 defaults are indistinguishable and both must go. A deletion failure is " +
					"reported as a WARNING, never as a failed create: the group is live either way.\n\n" +
					"This is create-time behaviour only. The value is carried in state so plans stay " +
					"clean, changing it on an existing group does nothing, and Read never lists or " +
					"manages rules. Defaults to `false` today; at provider v2 the default flips to " +
					"`true`, announced by a deprecation notice in the v1 line ahead of the flip.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
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
	// rules holds the group's rules when the create path already fetched them,
	// so the delete_default_egress opt-in below does not re-list. Nil means
	// "not fetched" — the helper lists them itself (the sync path's POST body
	// may not embed rules).
	var rules []apiSecurityGroupRule
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
		// The same read-back embeds the rules (it is the GET
		// frostmoln_security_group_rule reads through). A parse miss costs one
		// extra GET in the helper, nothing more.
		var withRules apiSecurityGroupWithRules
		if err := json.Unmarshal(readResp.Body, &withRules); err == nil {
			rules = withRules.Rules
		}
	} else if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Security Group Response", err.Error())
		return
	}

	if plan.DeleteDefaultEgress.ValueBool() {
		r.deleteDefaultEgressRules(ctx, sg.ID, rules, &resp.Diagnostics)
	}

	plan.fromAPI(ctx, &sg, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// ModifyPlan is the plan-time control for delete_default_egress's one lie:
// flipped on (or off) for a group that already exists, the change is a
// documented no-op — Create-time behaviour only, Update never touches rules.
// A schema description cannot say that during a CI apply, so the plan does.
func (r *securityGroupResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return // create or destroy: nothing to warn about
	}

	var planVal, stateVal types.Bool
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("delete_default_egress"), &planVal)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("delete_default_egress"), &stateVal)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if planVal.IsUnknown() || planVal.IsNull() || stateVal.IsNull() || planVal.Equal(stateVal) {
		return
	}

	resp.Diagnostics.AddWarning("delete_default_egress Is Create-Time Only",
		"This change takes no effect: the attribute is applied only when a security group is created, "+
			"and this group already exists. Its rules are untouched — the platform-injected default egress "+
			"rules remain exactly as they were, whichever way the value flipped. To remove them, delete "+
			"them outside Terraform or recreate the group. Terraform will not retry this on a later apply.")
}

// deleteDefaultEgressRules removes the allow-all egress pair the network
// service injects into every new group: one rule per address family, each
// with an EMPTY remote prefix and no remote group. The match is on
// direction+empty-prefix, never on family — a rule carries no ether_type, so
// the IPv4 and IPv6 defaults are indistinguishable and both must go.
//
// listed carries the rules when the caller already fetched them (the async
// create path's read-back embeds them); nil means list them here.
//
// The group was created moments ago, so the defaults are the only rules it
// can carry: no frostmoln_security_group_rule can have targeted it yet, and
// only the platform can create a rule with no remote at all (the API refuses
// one). The protocol guard keeps the match pinned to the allow-all pair even
// so. Every failure is a warning, never a failed create — the group is live,
// and failing here would strand it unmanaged.
func (r *securityGroupResource) deleteDefaultEgressRules(ctx context.Context, sgID string, listed []apiSecurityGroupRule, diags *diag.Diagnostics) {
	rules := listed
	if rules == nil {
		apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s", sgID)), nil)
		if err != nil {
			diags.AddWarning("Could Not List Default Egress Rules",
				fmt.Sprintf("Security group %s was created, but listing its rules to delete the platform-injected "+
					"default egress rules failed: %s. The two allow-all egress rules (IPv4 and IPv6) may still be "+
					"present — check the group outside Terraform and remove them if so. Terraform will not retry "+
					"this on a later apply — the deletion only happens at create.", sgID, err.Error()))
			return
		}

		var group apiSecurityGroupWithRules
		if err := json.Unmarshal(apiResp.Body, &group); err != nil {
			diags.AddWarning("Could Not List Default Egress Rules",
				fmt.Sprintf("Security group %s was created, but parsing its rule list to delete the platform-injected "+
					"default egress rules failed: %s. The two allow-all egress rules (IPv4 and IPv6) may still be "+
					"present — check the group outside Terraform and remove them if so. Terraform will not retry "+
					"this on a later apply — the deletion only happens at create.", sgID, err.Error()))
			return
		}
		rules = group.Rules
	}

	matched := 0
	for _, rule := range rules {
		if rule.Direction != "egress" || rule.RemoteCIDR != "" || rule.RemoteGroupID != "" {
			continue
		}
		// The injected pair is allow-all ("any protocol to everywhere"); a
		// narrower empty-remote rule could only be a future platform helper,
		// which is not this attribute's to delete.
		if rule.Protocol != "" && rule.Protocol != "any" {
			continue
		}
		matched++
		if _, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/security-groups/%s/rules/%s", sgID, rule.ID))); err != nil {
			diags.AddWarning("Could Not Delete A Default Egress Rule",
				fmt.Sprintf("Security group %s was created, but deleting the platform-injected default egress "+
					"rule %s failed: %s. It may still be present — check the group outside Terraform and "+
					"remove it if so. Terraform will not retry this on a later apply — the deletion only "+
					"happens at create.", sgID, rule.ID, err.Error()))
		}
	}

	// The pair is documented as unconditional, so finding nothing to delete
	// means the platform changed under the contract — say so, or the opt-in
	// reports success while the group egresses freely.
	if matched == 0 {
		diags.AddWarning("No Default Egress Rules Found",
			fmt.Sprintf("Security group %s was created with delete_default_egress, but none of its rules is "+
				"an egress rule with an empty remote prefix — the platform-injected allow-all pair this "+
				"attribute deletes was not there. The platform's behaviour may have changed; verify the "+
				"group's egress outside Terraform. Terraform will not retry this on a later apply — the "+
				"deletion only happens at create.", sgID))
	}
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
