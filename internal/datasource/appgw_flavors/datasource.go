// Package appgw_flavors implements the frostmoln_appgw_flavors data source.
package appgw_flavors

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ datasource.DataSource              = &flavorsDataSource{}
	_ datasource.DataSourceWithConfigure = &flavorsDataSource{}
)

// FlavorsModel is the data source's state.
type FlavorsModel struct {
	IncludeInactive types.Bool    `tfsdk:"include_inactive"`
	Flavors         []FlavorModel `tfsdk:"flavors"`
}

// FlavorModel is one Application Gateway size.
type FlavorModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`

	MaxListeners types.Int64 `tfsdk:"max_listeners"`
	MaxRoutes    types.Int64 `tfsdk:"max_routes"`
	MaxBackends  types.Int64 `tfsdk:"max_backends"`
	MaxWafRules  types.Int64 `tfsdk:"max_waf_rules"`

	MaxRPS         types.Int64 `tfsdk:"max_requests_per_second"`
	MaxConnections types.Int64 `tfsdk:"max_concurrent_connections"`

	PricingTier types.String `tfsdk:"pricing_tier"`
	Active      types.Bool   `tfsdk:"active"`
}

type apiFlavor struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`

	MaxListeners int `json:"maxListeners"`
	MaxRoutes    int `json:"maxRoutes"`
	MaxBackends  int `json:"maxBackends"`
	MaxWafRules  int `json:"maxWafRules"`

	MaxRPS         int `json:"maxRequestsPerSecond"`
	MaxConnections int `json:"maxConcurrentConnections"`

	PricingTier string `json:"pricingTier"`
	Active      bool   `json:"active"`
}

type apiFlavorListResponse struct {
	Flavors []apiFlavor `json:"flavors"`
}

type flavorsDataSource struct {
	client *client.Client
}

// NewDataSource returns a new flavors data source factory.
func NewDataSource() datasource.DataSource { return &flavorsDataSource{} }

func (d *flavorsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_flavors"
}

func (d *flavorsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Application Gateway size catalog.\n\n" +
			"A size is described by what the appliance carries, not by the virtual machine it runs " +
			"on: `vcpus`, `ram_mb` and `disk_gb` are no longer exposed, because they describe the " +
			"substrate rather than the service.\n\n" +
			"`max_listeners`, `max_routes`, `max_backends` and `max_waf_rules` are **structural " +
			"caps, not guidance**: exceeding one is refused. Size for the ruleset you intend to " +
			"write, not only for the traffic.",
		Attributes: map[string]schema.Attribute{
			"include_inactive": schema.BoolAttribute{
				Description: "Deprecated and inert. The customer catalog endpoint returns only " +
					"sizes that are currently offered, so there is nothing for this to include.",
				Optional:           true,
				DeprecationMessage: "include_inactive has no effect: the customer catalog never returns withdrawn sizes.",
			},
			"flavors": schema.ListNestedAttribute{
				Description: "The sizes on offer.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":            schema.StringAttribute{Description: "The flavor id, e.g. `agw.gp1.small`.", Computed: true},
						"name":          schema.StringAttribute{Description: "The display name.", Computed: true},
						"description":   schema.StringAttribute{Description: "What this size is for.", Computed: true},
						"max_listeners": schema.Int64Attribute{Description: "Maximum listeners. Enforced.", Computed: true},
						"max_routes":    schema.Int64Attribute{Description: "Maximum routes. Enforced.", Computed: true},
						"max_backends":  schema.Int64Attribute{Description: "Maximum backends. Enforced.", Computed: true},
						"max_waf_rules": schema.Int64Attribute{Description: "Maximum WAF rules. Enforced.", Computed: true},
						"max_concurrent_connections": schema.Int64Attribute{
							Description: "Concurrent connections this size is rated for, and the limit the " +
								"appliance is built with by default. NOT a hard cap — a listener with a " +
								"larger `max_connections` raises the appliance's global limit to meet it.",
							Computed: true,
						},
						"max_requests_per_second": schema.Int64Attribute{
							Description: "Rated throughput. NOT enforced and not a guarantee — real throughput " +
								"depends on your rules, TLS settings and backends. It is the basis " +
								"`max_concurrent_connections` is derived from.",
							Computed: true,
						},
						"pricing_tier": schema.StringAttribute{Description: "The pricing category this size bills under.", Computed: true},
						"active":       schema.BoolAttribute{Description: "Whether new gateways may be created on this size.", Computed: true},
					},
				},
			},
		},
	}
}

func (d *flavorsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	d.client = c
}

func (d *flavorsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg FlavorsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// TENANT-SCOPED, though the sizes are the same for every tenant. The offer is
	// entitlement-gated and an entitlement is held per tenant, so only a path
	// naming a tenant lets the gateway resolve the entitlement of the tenant this
	// provider is configured for (ADR-0052). Read from a path naming no tenant it
	// refused every org-invited user, whose home entitlement set is empty.
	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/application-gateways/catalog/flavors"), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Application Gateway Flavors", err.Error())
		return
	}
	list, err := client.ParseResponse[apiFlavorListResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Flavors Response", err.Error())
		return
	}
	// 🔴 NO CLIENT-SIDE FILTER, because there is nothing to filter. The customer
	// catalog handler hard-codes empty list options and the repository appends
	// `WHERE active`, so a withdrawn size never crosses the wire. A filter here
	// would read as a capability the platform does not have.
	cfg.Flavors = make([]FlavorModel, 0, len(list.Flavors))
	for _, f := range list.Flavors {
		cfg.Flavors = append(cfg.Flavors, FlavorModel{
			ID:             types.StringValue(f.ID),
			Name:           types.StringValue(f.Name),
			Description:    types.StringValue(f.Description),
			MaxListeners:   types.Int64Value(int64(f.MaxListeners)),
			MaxRoutes:      types.Int64Value(int64(f.MaxRoutes)),
			MaxBackends:    types.Int64Value(int64(f.MaxBackends)),
			MaxWafRules:    types.Int64Value(int64(f.MaxWafRules)),
			MaxRPS:         types.Int64Value(int64(f.MaxRPS)),
			MaxConnections: types.Int64Value(int64(f.MaxConnections)),
			PricingTier:    types.StringValue(f.PricingTier),
			Active:         types.BoolValue(f.Active),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
