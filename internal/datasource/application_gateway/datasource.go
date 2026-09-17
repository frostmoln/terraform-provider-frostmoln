// Package application_gateway implements the frostmoln_application_gateway
// Terraform data source: the name→id resolver for an Application Gateway —
// the lookup a configuration needs to reach a gateway that Terraform did not
// create. The offer's child resources (appgw_listener, appgw_backend_pool,
// appgw_certificate, appgw_waf_policy, appgw_config_apply, …) all take a
// REQUIRED gateway_id and have no other path to it, so this resolver is what
// lets a portal- or `fm`-provisioned gateway front a listener without a
// hardcoded UUID plumbed through terraform_remote_state.
//
// PROVENANCE (what is verified locally NOW, at this checkout):
//
//   - Edge route: api-gateway internal/service/definitions.go:3317 —
//     route("appgw-tenant-gateways", "/api/v1/tenants/*/application-gateways",
//     "GET", "POST") and route("appgw-tenant-gateway",
//     "/api/v1/tenants/*/application-gateways/*", "GET", "PATCH", "DELETE").
//     The provider's paths below match them.
//   - Client contract: fm-cli internal/appgw/service.go (List + Get; the
//     Gateway struct in internal/appgw/types.go) with the mock envelope
//     {"gateways":[...],"totalCount":N} pinned at
//     internal/appgw/service_test.go:117, plus the provider's own
//     frostmoln_application_gateway resource, whose apiGateway mirrors the
//     same wire shape.
//
// The Application Gateway SERVICE repository itself is not available locally,
// so service-side internals (how deletes render on the wire, whether tombstoned
// rows really surface in a list, pagination on the collection) carry the
// 2026-08-29 report-trusted caveat per the TERRAFORM-PROVIDER-CONVERGENCE-AUDIT
// §8/§9 framing — re-verify against the service repo when it is reachable.
//
// The shape is the house resolver shape (`frostmoln_vpc`, `frostmoln_postgres_instance`):
// `id` or `name`, exactly one; an id goes straight to the gateway read, a name
// lists the tenant's gateways and matches client-side. A name resolver must do
// the match client-side: the gateway list takes no query parameters at all
// (fm-cli's List sends none), so one request covers the tenant.
//
// Ambiguity and absence both fail the read. A resolver that silently picked
// the first of several same-named gateways would feed the wrong one's id into
// a listener that the tenant cannot delete through Terraform — exactly the
// mistake this data source exists to prevent — so more than one match is an
// error that names the colliding ids, and zero matches is an error, never an
// empty row. A tombstone never resolves: the application_gateway resource's
// Read treats status=="deleted" as gone (internal/resource/application_gateway/
// resource.go), because a soft-deleted gateway in state is a tombstone that
// never converges; this resolver applies the same rule on BOTH paths — a
// deleted row is skipped on the name path and reported absent on the id path.
package application_gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &applicationGatewayDataSource{}

// NewDataSource returns a new frostmoln_application_gateway data source factory.
func NewDataSource() datasource.DataSource {
	return &applicationGatewayDataSource{}
}

type applicationGatewayDataSource struct {
	client *client.Client
}

// applicationGatewayModel is the Terraform state model. Attribute names mirror
// the frostmoln_application_gateway resource's (plus the read-only config
// apply trail the resource does not carry), so a resolved gateway_id can be
// fed straight into a child resource without translating names.
type applicationGatewayModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Status            types.String `tfsdk:"status"`
	FlavorID          types.String `tfsdk:"flavor_id"`
	Version           types.String `tfsdk:"version"`
	VPCID             types.String `tfsdk:"vpc_id"`
	SubnetID          types.String `tfsdk:"subnet_id"`
	PrivateIP         types.String `tfsdk:"private_ip"`
	VPCCIDR           types.String `tfsdk:"vpc_cidr"`
	PublicIPMode      types.String `tfsdk:"public_ip_mode"`
	PublicIPID        types.String `tfsdk:"public_ip_id"`
	PublicIP          types.String `tfsdk:"public_ip"`
	WafPolicyID       types.String `tfsdk:"waf_policy_id"`
	AppliedWafVersion types.Int64  `tfsdk:"applied_waf_version"`
	ConfigGeneration  types.Int64  `tfsdk:"config_generation"`
	ConfigRevision    types.Int64  `tfsdk:"config_revision"`
	ConfigStatus      types.String `tfsdk:"config_status"`
	ConfigDetail      types.String `tfsdk:"config_detail"`
	ConfigAppliedAt   types.String `tfsdk:"config_applied_at"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
	TenantID          types.String `tfsdk:"tenant_id"`
}

// apiGateway is the API representation of one Application Gateway — the same
// wire shape the application_gateway resource maps (and fm-cli's appgw.Gateway).
//
// The timestamps stay STRINGS: the wire serves RFC3339, and the resource's
// apiGateway renders createdAt/updatedAt verbatim (resource.go/model.go) rather
// than decoding into time.Time and reformatting — so the data source passes the
// bytes through unchanged and the rendered value is exactly what the platform
// said. The same holds for configAppliedAt, with a pointer for the applied-at
// being genuinely absent until a first apply has succeeded.
type apiGateway struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	TenantID string `json:"tenantId,omitempty"`
	Status   string `json:"status"`
	FlavorID string `json:"flavorId"`
	Version  string `json:"version"`

	VPCID     string `json:"vpcId"`
	SubnetID  string `json:"subnetId"`
	PrivateIP string `json:"privateIp,omitempty"`
	VPCCIDR   string `json:"vpcCidr,omitempty"`

	// PublicIPMode is "allocated" or "selected". PublicIPID is echoed ONLY for
	// a bring-your-own address; a pool-allocated one has no id here.
	PublicIPMode string `json:"publicIpMode"`
	PublicIPID   string `json:"publicIpId,omitempty"`
	PublicIP     string `json:"publicIp,omitempty"`

	WafPolicyID       string `json:"wafPolicyId,omitempty"`
	AppliedWafVersion int    `json:"appliedWafVersion"`

	ConfigGeneration int64  `json:"configGeneration"`
	ConfigStatus     string `json:"configStatus"`
	ConfigRevision   *int64 `json:"configRevision,omitempty"`
	ConfigDetail     string `json:"configDetail,omitempty"`

	ConfigAppliedAt *string `json:"configAppliedAt,omitempty"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt,omitempty"`
	DeletedAt       *string `json:"deletedAt,omitempty"`
}

type apiGatewayList struct {
	Gateways   []apiGateway `json:"gateways"`
	TotalCount int          `json:"totalCount"`
}

func (d *applicationGatewayDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_application_gateway"
}

func (d *applicationGatewayDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up an Application Gateway by ID or name. Exactly one of `id` or " +
			"`name` must be specified.\n\n" +

			"This is how a configuration reaches a gateway that Terraform did not create: the " +
			"offer's child resources (`frostmoln_appgw_listener`, `frostmoln_appgw_backend_pool`, " +
			"`frostmoln_appgw_certificate`, `frostmoln_appgw_waf_policy`, " +
			"`frostmoln_appgw_config_apply`, …) all take a required `gateway_id`, and a gateway " +
			"provisioned in the portal or with `fm` can today be referenced only by a hardcoded " +
			"UUID. Look it up by name instead and the reference survives re-provisioning " +
			"elsewhere.\n\n" +

			"A name lookup that matches NOTHING fails the read, and so does one that matches " +
			"MORE THAN ONE gateway: the data source refuses to pick for you (the diagnostic " +
			"names the colliding ids). A deleted gateway never resolves — by id or by name — " +
			"the same rule the `frostmoln_application_gateway` resource's read applies.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the Application Gateway. Exactly one of " +
					"id or name must be specified.",
				Optional: true,
				Validators: []validator.String{
					validGatewayIDValidator{},
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the Application Gateway. Exactly one of id or name " +
					"must be specified.",
				Optional: true,
			},
			"status": schema.StringAttribute{
				Description: "The gateway's lifecycle state. A `deleted` row never satisfies " +
					"this data source.",
				Computed: true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor (size) of the gateway, e.g. `agw.gp1.small`.",
				Computed:    true,
			},
			"version": schema.StringAttribute{
				Description: "The appliance version the gateway runs. Platform-managed and " +
					"chosen by the server at create.",
				Computed: true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC the gateway lives in.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet the gateway's appliance attaches to.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The appliance's address inside the VPC, null while provisioning " +
					"has not yet reached the address.",
				Computed: true,
			},
			"vpc_cidr": schema.StringAttribute{
				Description: "The gateway's VPC range. Backend addresses are validated against it.",
				Computed:    true,
			},
			"public_ip_mode": schema.StringAttribute{
				Description: "Where the gateway's public address comes from: `allocated` (the " +
					"platform draws one from the pool and releases it with the gateway) or " +
					"`selected` (an address you hold, named by `public_ip_id`, never released).",
				Computed: true,
			},
			"public_ip_id": schema.StringAttribute{
				Description: "The Public IP id for a bring-your-own (`selected`) gateway; the " +
					"allocated address deliberately has no id here and is visible as " +
					"`public_ip` only.",
				Computed: true,
			},
			"public_ip": schema.StringAttribute{
				Description: "The gateway's public address, null while provisioning has not yet " +
					"reached the address.",
				Computed: true,
			},
			"waf_policy_id": schema.StringAttribute{
				Description: "The WAF policy attached to the gateway, null when none is attached.",
				Computed:    true,
			},
			"applied_waf_version": schema.Int64Attribute{
				Description: "The WAF ruleset version the appliance currently composes with.",
				Computed:    true,
			},
			"config_generation": schema.Int64Attribute{
				Description: "The configuration generation that has been AUTHORED.",
				Computed:    true,
			},
			"config_revision": schema.Int64Attribute{
				Description: "The configuration generation the appliance has ACKNOWLEDGED. Null " +
					"until something has been applied. When it differs from `config_generation` " +
					"there is a change the gateway is not yet serving.",
				Computed: true,
			},
			"config_status": schema.StringAttribute{
				Description: "What happened to the last configuration apply: `pending`, " +
					"`applying`, `applied`, `failed` or `unknown`. Distinct from `status`, " +
					"which is the appliance's lifecycle.",
				Computed: true,
			},
			"config_detail": schema.StringAttribute{
				Description: "The appliance's own words when it refused a configuration, null " +
					"when the last apply was accepted or nothing has been applied yet.",
				Computed: true,
			},
			"config_applied_at": schema.StringAttribute{
				Description: "When the appliance last acknowledged a configuration, null until " +
					"a first apply has succeeded.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the gateway was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the gateway was last updated, null when the " +
					"platform has not recorded one.",
				Computed: true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this gateway.",
				Computed:    true,
			},
		},
	}
}

func (d *applicationGatewayDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *applicationGatewayDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg applicationGatewayModel
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

	var gw *apiGateway
	if idSet {
		found, diags := d.readByID(ctx, cfg.ID.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		gw = found
	} else {
		found, diags := d.resolveByName(ctx, cfg.Name.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		gw = found
	}

	setInstanceState(&cfg, gw)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// readByID walks the id path: one gateway read, the identity guard, then the
// tombstone check. The identity guard refuses a 200 that does not carry the
// requested id — whatever answered is not the gateway read this provider
// builds its contract on.
func (d *applicationGatewayDataSource) readByID(ctx context.Context, id string) (*apiGateway, diag.Diagnostics) {
	var diags diag.Diagnostics

	pathStr, pathErr := gatewayPath(d.client, id)
	if pathErr != nil {
		diags.AddError("Invalid Gateway ID", pathErr.Error())
		return nil, diags
	}

	apiResp, err := d.client.Get(ctx, pathStr, nil)
	if err != nil {
		if client.IsNotFound(err) {
			diags.AddError(
				"The Application Gateway does not exist",
				fmt.Sprintf("The id %q answers 404 — there is no such Application Gateway (or "+
					"none this caller can see), so there is nothing to resolve. Correct `id`, "+
					"or look the gateway up by `name` instead.\n\n%s", id, err.Error()),
			)
			return nil, diags
		}
		diags.AddError("Failed to Read Application Gateway", err.Error())
		return nil, diags
	}

	var gw apiGateway
	if err := json.Unmarshal(apiResp.Body, &gw); err != nil {
		diags.AddError("Failed to Parse Application Gateway Response", err.Error())
		return nil, diags
	}
	if gw.ID != id {
		diags.AddError(
			"This gateway read did not identify the requested gateway",
			fmt.Sprintf("The response does not carry the id this path asks for (%q). Whatever "+
				"answered is not the gateway read this provider builds its contract on, so the "+
				"lookup refuses rather than render a row no service promised. The path was %q.",
				id, pathStr),
		)
		return nil, diags
	}
	// A soft-deleted gateway reads back, but with status "deleted": resolving
	// it would feed a tombstone's id into a child resource that can never
	// apply its configuration again — the same verdict the application_gateway
	// resource's Read renders (status "deleted" is treated as gone, not as a
	// row).
	if deletedGateway(&gw) {
		diags.AddError(
			"The Application Gateway does not exist",
			fmt.Sprintf("The id %q answers a deleted gateway (status %q), not a live one — a "+
				"deleted gateway is gone for this data source, whatever the read returned. "+
				"Correct `id`, or look the gateway up by `name` instead.", id, gw.Status),
		)
		return nil, diags
	}
	return &gw, diags
}

// resolveByName walks the list path. The list takes no query parameters at all
// (fm-cli's List sends none), so one request covers the tenant and the match
// happens client-side; more than one match is an error that names the
// colliding ids. A tombstone (status "deleted" or a deletedAt) is skipped, not
// matched: a deleted gateway never resolves, on this path or any other.
func (d *applicationGatewayDataSource) resolveByName(ctx context.Context, name string) (*apiGateway, diag.Diagnostics) {
	var diags diag.Diagnostics

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/application-gateways"), nil)
	if err != nil {
		diags.AddError("Failed to List Application Gateways", err.Error())
		return nil, diags
	}

	var list apiGatewayList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		diags.AddError("Failed to Parse Application Gateways Response", err.Error())
		return nil, diags
	}

	var matches []apiGateway
	for i := range list.Gateways {
		// A tombstone with the right name stays invisible: absent, not resolved
		// wrong — the same rule the id path enforces.
		if list.Gateways[i].Name == name && !deletedGateway(&list.Gateways[i]) {
			matches = append(matches, list.Gateways[i])
		}
	}

	switch len(matches) {
	case 0:
		// THE LIST HAS NO PAGE PARAMETERS, so this lookup is one request, and
		// its totalCount is the only window check there is: when the envelope
		// names more gateways than the single page carried, a zero-match
		// verdict is "not in the window we received", never the plain absence
		// the not-found arm asserts.
		if len(list.Gateways) < list.TotalCount {
			diags.AddError(
				fmt.Sprintf("No Application Gateway named %q in the returned window", name),
				fmt.Sprintf("The gateway list reported %d gateways but returned %d in the "+
					"window it carries, so the lookup cannot claim the name is absent — "+
					"there may be gateways beyond it. Resolve with `id` instead.", list.TotalCount, len(list.Gateways)),
			)
			return nil, diags
		}
		diags.AddError(
			"No Application Gateway with this name",
			fmt.Sprintf("No Application Gateway named %q resolves to a live gateway in this "+
				"tenant. Check the spelling of `name`; translate a deleted gateway's id out "+
				"of your configuration rather than resolving it again.", name),
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
			fmt.Sprintf("%d Application Gateways share the name %q", len(matches), name),
			fmt.Sprintf("The tenant's gateways carry more than one gateway with this name "+
				"(%s). The data source refuses to pick one for you: every match resolves to "+
				"a DIFFERENT gateway, and a silent pick would feed the wrong gateway's id "+
				"into your listeners, pools and certificates. Disambiguate with `id`, or "+
				"rename the gateways.", strings.Join(ids, ", ")),
		)
		return nil, diags
	}
}

// deletedGateway reports whether a wire row is a tombstone. The application
// gateway resource's Read treats status=="deleted" as gone; a set deletedAt is
// carried by the same verdict — both say the delete has reached this row.
func deletedGateway(g *apiGateway) bool {
	return g.Status == "deleted" || g.DeletedAt != nil
}

// gatewayPath builds one gateway read path behind the same guard the id
// carries at plan time — Read must never trust that a validated configuration
// is the only thing that reaches it.
func gatewayPath(c *client.Client, id string) (string, error) {
	if err := validGatewayID(id); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/application-gateways/%s", id)), nil
}

// validGatewayID refuses an id that cannot safely be one path segment. The
// same guard shape the fm-cli appgw service's tenantPath carries (and this
// package's sibling resolvers): a "." or ".." id does not stay one path
// segment (URL assembly CLEANS dot segments), and the cleaned URL addresses a
// DIFFERENT resource — measured upstream, `..` under a nested gateway path
// collapsed onto the gateway collection itself.
func validGatewayID(id string) error {
	if id == "" {
		return fmt.Errorf("a gateway ID is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\?#%`) {
		return fmt.Errorf("invalid gateway ID %q", id)
	}
	return nil
}

// validGatewayIDValidator carries validGatewayID into plan time, so a
// configuration with an unusable id fails before any request is built.
type validGatewayIDValidator struct{}

func (v validGatewayIDValidator) Description(_ context.Context) string {
	return "value must be a usable gateway ID: non-empty, and a single URL path segment"
}

func (v validGatewayIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validGatewayIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validGatewayID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Gateway ID",
			fmt.Sprintf("%s: %s", err.Error(), "a gateway ID must be non-empty and "+
				"must not contain a '/', a backslash, '?', '#' or '%', so it can only ever "+
				"address the one gateway named."),
		)
	}
}

// setInstanceState maps one verified gateway onto the model. Absent-optionals
// are null, never "" or 0 — a check block must be able to tell "no value"
// apart from "empty value". The always-on config trail (config_generation,
// config_status, applied_waf_version) renders as its wire value, zero included:
// a gateway with nothing authored sits at generation 0, and that zero is a real
// fact, not an absence.
func setInstanceState(state *applicationGatewayModel, gw *apiGateway) {
	state.ID = types.StringValue(gw.ID)
	state.Name = types.StringValue(gw.Name)
	state.Status = types.StringValue(gw.Status)
	state.FlavorID = types.StringValue(gw.FlavorID)
	state.Version = types.StringValue(gw.Version)
	state.VPCID = types.StringValue(gw.VPCID)
	state.SubnetID = types.StringValue(gw.SubnetID)
	state.PrivateIP = stringFromWire(gw.PrivateIP)
	state.VPCCIDR = stringFromWire(gw.VPCCIDR)
	state.PublicIPMode = stringFromWire(gw.PublicIPMode)
	state.PublicIPID = stringFromWire(gw.PublicIPID)
	state.PublicIP = stringFromWire(gw.PublicIP)
	state.WafPolicyID = stringFromWire(gw.WafPolicyID)
	state.AppliedWafVersion = types.Int64Value(int64(gw.AppliedWafVersion))
	state.ConfigGeneration = types.Int64Value(gw.ConfigGeneration)
	if gw.ConfigRevision != nil {
		state.ConfigRevision = types.Int64Value(*gw.ConfigRevision)
	} else {
		// Null, not zero: "never applied" and "applied revision 0" are
		// different facts and revision 0 is a real value — the same
		// treatment the application_gateway resource's fromAPI gives it.
		state.ConfigRevision = types.Int64Null()
	}
	state.ConfigStatus = stringFromWire(gw.ConfigStatus)
	state.ConfigDetail = stringFromWire(gw.ConfigDetail)
	state.ConfigAppliedAt = stringPtrFromWire(gw.ConfigAppliedAt)
	state.CreatedAt = stringFromWire(gw.CreatedAt)
	state.UpdatedAt = stringFromWire(gw.UpdatedAt)
	state.TenantID = stringFromWire(gw.TenantID)
}

// stringPtrFromWire maps an absent optional timestamp to null.
func stringPtrFromWire(v *string) types.String {
	if v == nil || *v == "" {
		return types.StringNull()
	}
	return types.StringValue(*v)
}

// stringFromWire maps an absent optional string to null.
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
