// Package security_group implements the frostmoln_security_group Terraform
// data source: the identity resolver for a security group — the lookup a
// configuration needs to reach a group that Terraform did not create (the
// platform-managed group a managed database, cache, webserver or messaging
// instance attaches, or a portal-provisioned one whose UUID is better
// referenced than hardcoded).
//
// The shape is the house resolver shape (`frostmoln_postgres_instance`): `id`
// or `name`, exactly one. An id goes straight to the group read. A name lists
// the tenant's groups using the list service's exact `name` filter, sent
// server-side (the `vpcId` filter rides the same request when the optional
// `vpc_id` input carries a value), and re-verifies the match client-side —
// the filter is the fast path, the re-verify is the contract.
//
// The schema deliberately carries NO `rules` collection: the rule listing is
// OWNED by the sibling data source `frostmoln_security_group_rules` (one
// collection, one owner — the Surface Contract doctrine), so this resolver's
// job is the id. Rules are read through it, with the group id this lookup
// resolves.
//
// Two visibility facts this package pins. Offer-internal groups — the ones
// carrying the reserved nlmeta tag — never appear in the customer list, so a
// name lookup cannot resolve one. But the tenant-VISIBLE platform-managed
// groups (the ones for a managed database, cache, webserver or messaging
// instance) DO appear and read fine from this data source; only their rules
// can never be managed, which `frostmoln_security_group_rules` documents in
// depth.
//
// Absence and ambiguity both fail the read. A resolver that silently picked
// the first of several same-named groups would attach the wrong group to a
// port — exactly the mistake this data source exists to prevent.
package security_group

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
)

var _ datasource.DataSource = &securityGroupDataSource{}

// NewDataSource returns a new frostmoln_security_group data source factory.
func NewDataSource() datasource.DataSource {
	return &securityGroupDataSource{}
}

type securityGroupDataSource struct {
	client *client.Client
}

// securityGroupModel is the Terraform state model. Attribute names mirror the
// frostmoln_security_group resource's, so a data source read can be fed
// straight into a reference without translating names.
type securityGroupModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	VPCID       types.String `tfsdk:"vpc_id"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	Tags        types.Map    `tfsdk:"tags"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
	TenantID    types.String `tfsdk:"tenant_id"`
}

// apiSecurityGroup is the API representation of one security group — the wire
// shape the /security-groups surface serves (the domain's SecurityGroup minus
// the children): rules stay on the wire but are deliberately NOT carried
// here; the sibling frostmoln_security_group_rules data source owns that
// listing.
type apiSecurityGroup struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	TenantID    string            `json:"tenantId"`
	VPCID       string            `json:"vpcId"`
	IsDefault   bool              `json:"isDefault"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt"`
}

// apiSecurityGroupList is the list envelope — securityGroups, totalCount and
// nextMarker, the platform-standard keys (only the load-balancer list
// deviates, counting on `total`). The list hides offer-internal groups
// server-side: tenant-visible platform-managed groups still come through.
type apiSecurityGroupList struct {
	SecurityGroups []apiSecurityGroup `json:"securityGroups"`
	TotalCount     int                `json:"totalCount"`
	NextMarker     string             `json:"nextMarker,omitempty"`
}

func (d *securityGroupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func (d *securityGroupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a security group by ID or name. Exactly one of `id` or `name` " +
			"must be specified.\n\n" +

			"This is how a configuration reaches a security group that Terraform did not create — " +
			"including the platform-managed group a managed database, cache, webserver or " +
			"messaging instance attaches (search the tenant's list by name, or pass its id). A " +
			"name lookup that matches NOTHING fails the read, and so does one that matches MORE " +
			"THAN ONE group: the data source refuses to pick for you (the diagnostic names the " +
			"colliding ids). The optional `vpc_id` narrows a name lookup (the `vpcId` filter " +
			"rides the request) and is echoed back from the read.\n\n" +

			"There is no `rules` attribute here, on purpose: the rule listing is OWNED by the " +
			"sibling data source `frostmoln_security_group_rules` (one collection, one owner), " +
			"so this resolver's job is the id the rules data source is addressed by.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the security group. Exactly one of " +
					"id or name must be specified.",
				Optional: true,
				Validators: []validator.String{
					validSecurityGroupIDValidator{},
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the security group. Exactly one of id or name " +
					"must be specified. A name is at most 234 characters (the resource's " +
					"validators enforce the same limit). If two VPCs could both carry the " +
					"name, pass `vpc_id` as well — an ambiguous match fails the read.",
				Optional: true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC the security group belongs to. As an INPUT it narrows a " +
					"name lookup (sent as the list's `vpcId` filter); in the result it is the " +
					"observed VPC of the resolved group.",
				Optional: true,
				Computed: true,
			},
			"description": schema.StringAttribute{
				Description: "The description of the security group (at most 1024 characters — " +
					"the limit the resource's own validators enforce), null when it has none.",
				Computed: true,
			},
			"is_default": schema.BoolAttribute{
				Description: "Whether this is the default security group.",
				Computed:    true,
			},
			"tags": schema.MapAttribute{
				Description: "The customer tags on the security group. Platform-reserved " +
					"frostmoln_* keys are filtered, matching the resource read-back.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the security group was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the security group was last updated, null when " +
					"the platform has not recorded one.",
				Computed: true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this security group.",
				Computed:    true,
			},
		},
	}
}

func (d *securityGroupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *securityGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg securityGroupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idSet := !cfg.ID.IsNull() && !cfg.ID.IsUnknown()
	nameSet := !cfg.Name.IsNull() && !cfg.Name.IsUnknown()

	if !idSet && !nameSet {
		resp.Diagnostics.AddError(
			"One of id or name must be specified",
			"Neither `id` nor `name` carries a value, so there is nothing to look up. "+
				"Specify exactly one of them.",
		)
		return
	}
	if idSet && nameSet {
		resp.Diagnostics.AddError(
			"Only one of id or name may be specified",
			fmt.Sprintf("Both `id` (%q) and `name` (%q) carry a value. Specify exactly one of "+
				"them.", cfg.ID.ValueString(), cfg.Name.ValueString()),
		)
		return
	}

	vpcIDFilter := ""
	if !cfg.VPCID.IsNull() && !cfg.VPCID.IsUnknown() {
		vpcIDFilter = cfg.VPCID.ValueString()
	}

	if idSet {
		sg, diags := d.readByID(ctx, cfg.ID.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		d.setInstanceState(ctx, &cfg, sg, resp)
		return
	}

	sg, diags := d.resolveByName(ctx, cfg.Name.ValueString(), vpcIDFilter)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	d.setInstanceState(ctx, &cfg, sg, resp)
}

// readByID walks the id path: one group read, then the identity guard. A 404
// here is one cause among more than it looks: the guardManagedSG middleware
// answers the SAME 404 for an offer-internal group it hides from this plane
// as the service answers for a genuinely absent one, so the diagnostic says
// both things rather than promising which. A 200 that does not carry the
// requested id is not the group read this provider builds its contract on,
// whatever answered.
func (d *securityGroupDataSource) readByID(ctx context.Context, id string) (*apiSecurityGroup, diag.Diagnostics) {
	var diags diag.Diagnostics

	pathStr, pathErr := securityGroupPath(d.client, id)
	if pathErr != nil {
		diags.AddError("Invalid Security Group ID", pathErr.Error())
		return nil, diags
	}

	apiResp, err := d.client.Get(ctx, pathStr, nil)
	if err != nil {
		if client.IsNotFound(err) {
			diags.AddError(
				"The security group does not exist",
				fmt.Sprintf("The id %q answers 404 — there is no such security group (or none "+
					"this caller can see). The platform also hides offer-internal groups from "+
					"this plane, so the 404 may be one of those rather than empty space. "+
					"Correct `id`, or look the group up by `name` instead.\n\n%s",
					id, err.Error()),
			)
			return nil, diags
		}
		diags.AddError("Failed to Read Security Group", err.Error())
		return nil, diags
	}

	var sg apiSecurityGroup
	if err := json.Unmarshal(apiResp.Body, &sg); err != nil {
		diags.AddError("Failed to Parse Security Group Response", err.Error())
		return nil, diags
	}
	if sg.ID != id {
		diags.AddError(
			"This security group read did not identify the requested security group",
			fmt.Sprintf("The response does not carry the id this path asks for (%q). Whatever "+
				"answered is not the security-group read this provider builds its contract on, "+
				"so the lookup refuses rather than render a row no service promised. The path "+
				"was %q.", id, pathStr),
		)
		return nil, diags
	}
	return &sg, diags
}

// resolveByName walks the list path. The exact `name` filter is the wire's own
// (`?name=`), and the optional `vpcId` filter rides the same request; but the
// filters are the FAST PATH, never the contract — Read re-verifies every row
// it answers with client-side, the way `frostmoln_subnet` does with its un-
// filtered list. The list pages on a marker, so Read walks the pages until
// the list is exhausted; more than one match is an error that names the
// colliding ids.
func (d *securityGroupDataSource) resolveByName(ctx context.Context, name, vpcID string) (*apiSecurityGroup, diag.Diagnostics) {
	var diags diag.Diagnostics

	var matches []apiSecurityGroup
	const pageSize = 100
	const maxPages = 20
	marker := ""
	// searchExhausted records the walk hitting the page cap with a NEXT MARKER
	// still pending: the tenant may carry groups beyond the search window, so
	// a zero-match verdict then is "not in the window we searched", never the
	// plain absence the not-found arm asserts.
	searchExhausted := false
	for page := 0; page < maxPages; page++ {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("name", name)
		if vpcID != "" {
			q.Set("vpcId", vpcID)
		}
		if marker != "" {
			q.Set("marker", marker)
		}

		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/security-groups"), q)
		if err != nil {
			diags.AddError("Failed to List Security Groups", err.Error())
			return nil, diags
		}

		var list apiSecurityGroupList
		if err := json.Unmarshal(apiResp.Body, &list); err != nil {
			diags.AddError("Failed to Parse Security Groups Response", err.Error())
			return nil, diags
		}

		for i := range list.SecurityGroups {
			sg := &list.SecurityGroups[i]
			if sg.Name != name {
				continue
			}
			if vpcID != "" && sg.VPCID != vpcID {
				continue
			}
			matches = append(matches, *sg)
		}
		if len(list.SecurityGroups) < pageSize || list.NextMarker == "" {
			break
		}
		if page == maxPages-1 {
			// The loop is about to end on the cap, and the server still has
			// more: the window, not the tenant, is what was searched.
			searchExhausted = true
		}
		marker = list.NextMarker
	}

	switch len(matches) {
	case 0:
		if searchExhausted {
			diags.AddError(
				fmt.Sprintf("No security group named %q in the searched window", name),
				fmt.Sprintf("The tenant's security-group list did not terminate within the "+
					"search window (%d pages), so the lookup cannot claim the name is "+
					"absent — there may be groups beyond it. Narrow via `id`, or with "+
					"`vpc_id` when the lookup can be pinned to one VPC.", maxPages),
			)
			return nil, diags
		}
		detail := fmt.Sprintf("No security group named %q resolves in this tenant. Check the "+
			"spelling of `name`.", name)
		if vpcID != "" {
			detail = fmt.Sprintf("No security group named %q resolves in VPC %q of this tenant. "+
				"Check the spelling of `name`, that `vpc_id` names the right VPC, or drop "+
				"`vpc_id` to search the whole tenant.", name, vpcID)
		}
		diags.AddError(
			"No security group with this name",
			detail,
		)
		return nil, diags
	case 1:
		return &matches[0], diags
	default:
		ids := make([]string, 0, len(matches))
		for i := range matches {
			ids = append(ids, matches[i].ID)
		}
		diags.AddError(
			fmt.Sprintf("%d security groups share the name %q", len(matches), name),
			fmt.Sprintf("The tenant's groups carry more than one with this name (%s). The "+
				"data source refuses to pick one for you: every match resolves to a DIFFERENT "+
				"group, and a silent pick would attach the wrong group to a port. Disambiguate "+
				"with `id`, or rename the groups.", strings.Join(ids, ", ")),
		)
		return nil, diags
	}
}

// securityGroupPath builds one group read path behind the same guard the id
// carries at plan time — Read must never trust that a validated configuration
// is the only thing that reaches it.
func securityGroupPath(c *client.Client, id string) (string, error) {
	if err := validSecurityGroupID(id); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/security-groups/%s", id)), nil
}

// validSecurityGroupID refuses an id that cannot safely be one path segment.
// The same guard shape the postgres_instance resolver carries: a "." or ".."
// id does not stay one path segment (the client joins with path.Join, which
// CLEANS), and the cleaned URL addresses a DIFFERENT resource.
func validSecurityGroupID(id string) error {
	if id == "" {
		return fmt.Errorf("a security group ID is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\?#%`) {
		return fmt.Errorf("invalid security group ID %q", id)
	}
	return nil
}

// validSecurityGroupIDValidator carries validSecurityGroupID into plan time,
// so a configuration with an unusable id fails before any request is built.
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
			fmt.Sprintf("%s: %s", err.Error(), "a security group ID must be non-empty and "+
				"must not contain a '/', a backslash, '?', '#' or '%', so it can only ever "+
				"address the one group named."),
		)
	}
}

// setInstanceState maps one verified group onto the model. Absent-optionals
// are null, never "" — a check block must be able to tell "no value" apart
// from "empty value". Tag filtering matches the resource read-back: the
// platform-reserved frostmoln_* keys are not customer tags and no config can
// converge on them. The `vpc_id` input becomes the observed VPC here, exactly
// as frostmoln_subnet's does.
func (d *securityGroupDataSource) setInstanceState(ctx context.Context, state *securityGroupModel, sg *apiSecurityGroup, resp *datasource.ReadResponse) {
	state.ID = types.StringValue(sg.ID)
	state.Name = types.StringValue(sg.Name)
	state.Description = securityGroupStringFromWire(sg.Description)
	state.VPCID = securityGroupStringFromWire(sg.VPCID)
	state.IsDefault = types.BoolValue(sg.IsDefault)
	state.CreatedAt = types.StringValue(sg.CreatedAt)
	state.UpdatedAt = securityGroupStringFromWire(sg.UpdatedAt)
	state.TenantID = types.StringValue(sg.TenantID)

	// Filter platform-reserved frostmoln_* keys so the computed tags attribute
	// exposes only customer tags, matching the resource read-back. `tags`
	// means the same thing on every surface.
	userTags := reservedmeta.FilterNetwork(sg.Tags)
	if len(userTags) > 0 {
		tagsMap, diags := types.MapValueFrom(ctx, types.StringType, userTags)
		resp.Diagnostics.Append(diags...)
		state.Tags = tagsMap
	} else {
		state.Tags = types.MapNull(types.StringType)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// securityGroupStringFromWire maps an empty optional string to null, not "".
func securityGroupStringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
