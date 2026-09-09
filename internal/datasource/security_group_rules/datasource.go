// Package security_group_rules implements the frostmoln_security_group_rules
// Terraform data source: the read-only listing of one security group's whole
// stored rule set — the drift detector the per-rule resource cannot be.
package security_group_rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &securityGroupRulesDataSource{}

// NewDataSource returns a new frostmoln_security_group_rules data source factory.
func NewDataSource() datasource.DataSource {
	return &securityGroupRulesDataSource{}
}

type securityGroupRulesDataSource struct {
	client *client.Client
}

// securityGroupRulesModel is the Terraform state model for the rule listing.
type securityGroupRulesModel struct {
	SecurityGroupID types.String `tfsdk:"security_group_id"`
	ID              types.String `tfsdk:"id"`
	Rules           types.List   `tfsdk:"rules"`
}

// securityGroupRuleRowModel is one row of the listing. The null experiments
// match the resource's model (internal/resource/security_group_rule): absent
// ports and remotes are null, never empty strings or zeroes — a check block
// must be able to tell "no remote" apart from "empty remote".
type securityGroupRuleRowModel struct {
	ID            types.String `tfsdk:"id"`
	Direction     types.String `tfsdk:"direction"`
	Protocol      types.String `tfsdk:"protocol"`
	PortRangeMin  types.Int64  `tfsdk:"port_range_min"`
	PortRangeMax  types.Int64  `tfsdk:"port_range_max"`
	RemoteCIDR    types.String `tfsdk:"remote_cidr"`
	RemoteGroupID types.String `tfsdk:"remote_group_id"`
	Description   types.String `tfsdk:"description"`
}

// apiSecurityGroupRule is the API representation of one rule, row for row what
// the resource's model carries (internal/resource/security_group_rule) — the
// same wire shape, so rows can pass through verbatim. The platform has no
// `ether_type` on the wire, so the IPv4 and IPv6 halves of the injected
// egress pair are indistinguishable here exactly as they are everywhere else.
type apiSecurityGroupRule struct {
	ID            string `json:"id"`
	Direction     string `json:"direction"`
	Protocol      string `json:"protocol"`
	PortRangeMin  *int   `json:"portRangeMin,omitempty"`
	PortRangeMax  *int   `json:"portRangeMax,omitempty"`
	RemoteCIDR    string `json:"remoteCidr,omitempty"`
	RemoteGroupID string `json:"remoteSecurityGroupId,omitempty"`
	Description   string `json:"description,omitempty"`
}

// apiSecurityGroupWithRules is what the group read answers with — the ONLY list
// of a group's rules there is (rules are not directly GETtable; the resource's
// Read and the async-create read-back use the same endpoint).
//
// `Rules` is a POINTER so "no answer about the set" stays distinguishable from
// an EMPTY set — but the discriminator CANNOT be key presence, unlike the
// routes listing. The route-collection read could key on presence because the
// router's domain explicitly keeps its set key on an empty tenant view (the
// non-nil TenantView, no omitempty — network/internal/domain/router.go). The
// network's security-group domain carries the set as `json:"rules,omitempty"`
// (network/internal/domain/security_group.go), so a healthy group with ZERO
// rules answers WITHOUT the key, omitempty having dropped it. Here Read falls
// back on identity: the body naming the requested group (see the guard in
// Read). The resource's own copy (internal/resource/security_group_rule) uses
// a plain slice because it only ever picks one rule by id, where the bluntness
// is safe; here the bluntness would merge "not the contract" into `rules = []`,
// which every check block reads as "no drift".
type apiSecurityGroupWithRules struct {
	ID    string                  `json:"id"`
	Rules *[]apiSecurityGroupRule `json:"rules"`
}

func (d *securityGroupRulesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group_rules"
}

func (d *securityGroupRulesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists every rule stored on one security group — the rows the rules panel " +
			"and `fm` would show, rendered exactly as `frostmoln_security_group_rule` writes " +
			"them.\n\n" +

			"This is the drift detector the per-rule resource cannot be. `frostmoln_security_group_rule` " +
			"is one resource per rule, and the parent `frostmoln_security_group` manages the group " +
			"and nothing else, so a rule added out of band (in the portal, with `fm`, through the " +
			"API, or by a colleague) is invisible to every plan: no resource holds its id, nothing " +
			"looks for it, and it appears in no plan and is removed by no apply. Rules are not even " +
			"directly GETtable — the group read itself is the only list — so this data source reads " +
			"the group's WHOLE stored set and renders it verbatim, and a `check` block or " +
			"`lifecycle` postcondition over the listing turns an out-of-band addition into a failed " +
			"plan (see the example and the surface-contract guide). Rules are additive — traffic " +
			"matching ANY rule is allowed — so every unplanned row makes the group more permissive " +
			"than the configuration says, and one world-open rule is as dangerous as the set of them.\n\n" +

			"WHAT A NULL REMOTE MEANS: a row whose `remote_cidr` and `remote_group_id` are BOTH " +
			"null constrains the source not at all. On EGRESS that row is normally one of the two " +
			"platform-injected allow-all egress rules every group carries until deleted — unmanaged " +
			"by Terraform, benign, and documented in the `frostmoln_security_group_rule` description. " +
			"On INGRESS it means the rule allows traffic from ANY source. The platform refuses to " +
			"create a rule with no remote today, so an ingress row with both remotes null predates " +
			"that refusal and is a world-open hole to close, not a cosmetic curiosity. The listing " +
			"renders what is stored, verbatim — surfacing these rows is this data source's job, " +
			"never hiding them.\n\n" +

			"GROUPS THE PLATFORM PROVISIONED FOR MANAGED SERVICES READ FINE AND CAN NEVER BE " +
			"MANAGED. The parent groups for a managed database, cache, webserver, messaging " +
			"instance, Kubernetes cluster or Application Gateway are visible in your account, so " +
			"this listing happily shows their rules — but every rule write on one is refused with " +
			"`409` / `resource_in_use`, permanently. Read them to know what the platform enforces; " +
			"point `frostmoln_security_group_rule` resources and imports only at groups you created.\n\n" +

			"IMPORT RECIPE: bring an out-of-band rule under management with `terraform import " +
			"frostmoln_security_group_rule.<name> <security_group_id>/<rule_id>`. One exception — " +
			"the injected allow-all egress pair: import it ONLY IN ORDER TO DESTROY IT, and remove " +
			"the block from configuration once the destroy has run. Leaving the block re-creates a " +
			"rule the platform refuses (no rule may be created with an empty remote), so the apply " +
			"would fail forever after; `frostmoln_security_group`'s `delete_default_egress` and the " +
			"resource description carry the full contract.",
		Attributes: map[string]schema.Attribute{
			"security_group_id": schema.StringAttribute{
				Description: "The ID of the security group whose stored rules to list.",
				Required:    true,
				Validators: []validator.String{
					// Refusing here, at plan time, means a mistyped id fails the
					// configuration instead of becoming a request with a meaningful
					// neighbor: a "." or ".." id would not stay one path segment, and
					// the cleaned URL addresses a DIFFERENT resource (see
					// internal/resource/vpc_route, where this guard was earned — the
					// SG surface runs bare fmt.Sprintf paths today, which is exactly
					// why the guard is inherited here rather than trusted to exist).
					validSecurityGroupIDValidator{},
				},
			},
			"id": schema.StringAttribute{
				Description: "The identifier of this listing — the same security group id as " +
					"`security_group_id`, deliberately mirrored rather than distinct: one group's " +
					"stored rule set is the whole identity of this listing, and there is no " +
					"server-side id beyond it.",
				Computed: true,
			},

			"rules": schema.ListNestedAttribute{
				Description: "Every rule stored on the group, in the rendering the platform " +
					"reports back: exactly the rows `frostmoln_security_group_rule` can see, " +
					"including rows nothing in the configuration declares. A group with no rules " +
					"renders as an empty list, never null. Row order is the platform's reporting " +
					"order and is not guaranteed stable — pin on rule ids, never on order.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The unique identifier of the rule — the id an import of " +
								"`frostmoln_security_group_rule` addresses it by.",
							Computed: true,
						},
						"direction": schema.StringAttribute{
							Description: "The direction of the rule: ingress or egress.",
							Computed:    true,
						},
						"protocol": schema.StringAttribute{
							Description: "The protocol: tcp, udp, icmp, or any.",
							Computed:    true,
						},
						"port_range_min": schema.Int64Attribute{
							Description: "The minimum port number. Null when the rule carries no " +
								"port range (an `any` or `icmp` rule, or one of the injected " +
								"allow-all egress rules).",
							Computed: true,
						},
						"port_range_max": schema.Int64Attribute{
							Description: "The maximum port number. Null when the rule carries no " +
								"port range.",
							Computed: true,
						},
						"remote_cidr": schema.StringAttribute{
							Description: "The remote CIDR the rule allows traffic from (ingress) or " +
								"to (egress). Null when the rule names no CIDR. When `remote_group_id` " +
								"is ALSO null: unqualified on egress (the injected allow-all pair), " +
								"world-open on ingress — see WHAT A NULL REMOTE MEANS above.",
							Computed: true,
						},
						"remote_group_id": schema.StringAttribute{
							Description: "The remote security group the rule allows traffic from " +
								"(ingress) or to (egress). Null when the rule names no remote group; " +
								"both remotes null is the case `remote_cidr` describes.",
							Computed: true,
						},
						"description": schema.StringAttribute{
							Description: "The description stored on the rule, null when unset.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *securityGroupRulesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}

	d.client = c
}

func (d *securityGroupRulesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg securityGroupRulesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sgID := cfg.SecurityGroupID.ValueString()

	// The schema validator catches a bad id at plan time; this is the same guard
	// at the request boundary, because Read must never trust that a validated
	// configuration is the only thing that reaches it.
	sgPath, pathErr := securityGroupPath(d.client, sgID)
	if pathErr != nil {
		resp.Diagnostics.AddError("Invalid Security Group ID", pathErr.Error())
		return
	}

	apiResp, err := d.client.Get(ctx, sgPath, nil)
	if err != nil {
		// THE 404 WITH ONE CAUSE — deliberately NOT the two-cause machine the
		// routes listing ports. There is no "security-group management disabled"
		// deployment mode on this surface, so a FLAT 404 from the group read is
		// the service's collapse doctrine in one voice: the group is absent or
		// not visible to this caller, and both mean stop. A data source has no
		// state to drop; the honest answer is to fail the read, never to render
		// `rules = []` — which every check block would read as "no drift".
		// A NESTED 404 (the api-gateway's unrouted-path envelope) is not a
		// verdict at all and falls to the generic arm below.
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError(
				"The security group does not exist",
				fmt.Sprintf("The group %q answers 404 — there is no such security group (or none "+
					"this caller can see), so there are no rules to list. Correct "+
					"`security_group_id`, or create the group.\n\n"+err.Error(), sgID),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Security Group", err.Error())
		return
	}

	var sg apiSecurityGroupWithRules
	if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Security Group Response", err.Error())
		return
	}

	// THE 200 MUST NAME THE GROUP THE PATH ASKS FOR. A key-presence guard (the
	// route listing's "did not answer with a route set") is NOT portable to
	// this surface: the network domain carries the rule set as
	// `json:"rules,omitempty"` (network/internal/domain/security_group.go), so
	// a healthy group with ZERO rules answers WITHOUT the key — refusing that
	// shape would fail every plan over a group whose rule set is legitimately
	// empty (delete_default_egress did its job, the injected pair
	// import-destroyed). The discriminator is identity, not presence: this
	// provider asked for one group, the group read identifies itself with
	// `id`, and an answer that names that group IS the contract — with the
	// set, or without it. Anything that does not name the group is refused,
	// never rendered as an empty table.
	if sg.ID != sgID {
		resp.Diagnostics.AddError(
			"This security group's read did not identify the requested group",
			fmt.Sprintf("The response does not carry the id of the security group this path asks "+
				"for, %q. Whatever answered is not the group read this provider builds its "+
				"contract on, so the listing refuses rather than render a table no service "+
				"promised.\n\n"+
				"The group path was %q. Check the deployment and retry; the error persists if "+
				"what answers this path is not the network API.", sgID, sgPath),
		)
		return
	}

	// An identified group with an ABSENT or NULL set is the genuinely empty
	// set — the platform's omitempty contract dropping the key, not a refusal.
	// So render `[]`, never null: to a check block those are different answers,
	// and null is the dishonest one.
	var rules []apiSecurityGroupRule
	if sg.Rules != nil {
		rules = *sg.Rules
	}
	rows := make([]securityGroupRuleRowModel, 0, len(rules))
	for i := range rules {
		// VERBATIM, on purpose. The platform renders back exactly what it stores,
		// and the empty remote stays null — including the fail-open ingress rows
		// and the injected egress pair. The listing's value is showing the set as
		// it is, disagreeable rows included (see WHAT A NULL REMOTE MEANS).
		rule := &rules[i]
		rows = append(rows, securityGroupRuleRowModel{
			ID:            types.StringValue(rule.ID),
			Direction:     types.StringValue(rule.Direction),
			Protocol:      types.StringValue(rule.Protocol),
			PortRangeMin:  int64FromWire(rule.PortRangeMin),
			PortRangeMax:  int64FromWire(rule.PortRangeMax),
			RemoteCIDR:    stringFromWire(rule.RemoteCIDR),
			RemoteGroupID: stringFromWire(rule.RemoteGroupID),
			Description:   stringFromWire(rule.Description),
		})
	}

	rulesList, listDiags := types.ListValueFrom(ctx, rowObjectType(), rows)
	resp.Diagnostics.Append(listDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := securityGroupRulesModel{
		SecurityGroupID: types.StringValue(sgID),
		ID:              types.StringValue(sgID),
		Rules:           rulesList,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// securityGroupPath is the same group read the resource's Read and the
// async-create read-back use (rules are not directly GETtable, so the group
// read embedding them is the only list there is).
func securityGroupPath(c *client.Client, sgID string) (string, error) {
	if err := validSecurityGroupID(sgID); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/security-groups/%s", sgID)), nil
}

// validSecurityGroupID refuses an id that cannot safely be one path segment. A
// deliberate copy of the vpc_routes guard (both listings build URLs the same
// way, so both must refuse the same values — keep them in step); the SG
// resources run no such guard today, which changes nothing about what a data
// source must refuse at ITS boundary.
func validSecurityGroupID(sgID string) error {
	if sgID == "" {
		return fmt.Errorf("a security group ID is required")
	}
	if sgID == "." || sgID == ".." || strings.ContainsAny(sgID, `/\?#%`) {
		return fmt.Errorf("invalid security group ID %q", sgID)
	}
	return nil
}

// validSecurityGroupIDValidator carries validSecurityGroupID into plan time, so
// a configuration with an unusable id fails before any request is built.
type validSecurityGroupIDValidator struct{}

func (v validSecurityGroupIDValidator) Description(_ context.Context) string {
	return "value must be a usable security group ID: non-empty, and a single URL path segment"
}

func (v validSecurityGroupIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validSecurityGroupIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validSecurityGroupID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Security Group ID",
			fmt.Sprintf("%s: %s", err.Error(), "a security group ID must be non-empty and must not "+
				"contain a '/', a backslash, '?', '#' or '%', so it can only ever address the one "+
				"security group named."),
		)
	}
}

// int64FromWire maps an absent optional port to null, not zero — a check block
// must be able to tell "no port range" from "port 0" (same mapping as the
// resource's fromAPI).
func int64FromWire(v *int) types.Int64 {
	if v == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*v))
}

// stringFromWire maps an empty optional string to null, not "" — the empty
// remote is what WHAT A NULL REMOTE MEANS turns into a verdict, and "" would
// blur it (same mapping as the resource's fromAPI).
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// rowObjectType is the framework type of one `rules` row.
func rowObjectType() attr.Type {
	return types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"id":              types.StringType,
			"direction":       types.StringType,
			"protocol":        types.StringType,
			"port_range_min":  types.Int64Type,
			"port_range_max":  types.Int64Type,
			"remote_cidr":     types.StringType,
			"remote_group_id": types.StringType,
			"description":     types.StringType,
		},
	}
}
