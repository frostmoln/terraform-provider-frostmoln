// Package dns_zone implements the frostmoln_dns_zone Terraform data source.
package dns_zone

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// normalizeZoneName matches the backend's NormalizeDNSZoneName: lowercase, trim,
// and ensure a single trailing dot, so a name lookup written as "Example.com"
// still matches the stored "example.com.".
func normalizeZoneName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "" && !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

var _ datasource.DataSource = &dnsZoneDataSource{}

// NewDataSource returns a new frostmoln_dns_zone data source factory.
func NewDataSource() datasource.DataSource {
	return &dnsZoneDataSource{}
}

type dnsZoneDataSource struct {
	client *client.Client
}

// dnsZoneModel is the Terraform state model for a DNS zone data source.
type dnsZoneModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Email       types.String `tfsdk:"email"`
	Description types.String `tfsdk:"description"`
	TTL         types.Int64  `tfsdk:"ttl"`
	Type        types.String `tfsdk:"type"`
	Status      types.String `tfsdk:"status"`
	Serial      types.Int64  `tfsdk:"serial"`
	RecordCount types.Int64  `tfsdk:"record_count"`
	NameServers types.List   `tfsdk:"name_servers"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

// apiDNSZone is the API representation of a DNS zone.
type apiDNSZone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Serial      uint32   `json:"serial"`
	TTL         int      `json:"ttl"`
	RecordCount int      `json:"recordCount"`
	NameServers []string `json:"nameServers,omitempty"`
	CreatedAt   string   `json:"createdAt"`
}

// apiDNSZoneList is the API response for listing DNS zones.
type apiDNSZoneList struct {
	Zones []apiDNSZone `json:"zones"`
}

func (d *dnsZoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (d *dnsZoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a managed DNS zone by ID or name, e.g. to read its delegation name_servers.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the zone. Exactly one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "The zone name (FQDN ending with a dot). Exactly one of id or name must be specified.",
				Optional:    true,
				Computed:    true,
			},
			"email": schema.StringAttribute{
				Description: "The SOA administrative contact email.",
				Computed:    true,
			},
			"description": schema.StringAttribute{
				Description: "The description of the zone.",
				Computed:    true,
			},
			"ttl": schema.Int64Attribute{
				Description: "The default record TTL in seconds.",
				Computed:    true,
			},
			"type": schema.StringAttribute{
				Description: "The zone type.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The status of the zone.",
				Computed:    true,
			},
			"serial": schema.Int64Attribute{
				Description: "The SOA serial of the zone.",
				Computed:    true,
			},
			"record_count": schema.Int64Attribute{
				Description: "The number of editable records in the zone.",
				Computed:    true,
			},
			"name_servers": schema.ListAttribute{
				Description: "The zone's delegation name servers. Delegate your domain at your registrar to exactly these.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the zone was created.",
				Computed:    true,
			},
		},
	}
}

func (d *dnsZoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *dnsZoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state dnsZoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idSet := !state.ID.IsNull() && !state.ID.IsUnknown()
	nameSet := !state.Name.IsNull() && !state.Name.IsUnknown()

	if !idSet && !nameSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Missing Attribute",
			"Exactly one of id or name must be specified.",
		)
		return
	}
	if idSet && nameSet {
		resp.Diagnostics.AddAttributeError(
			path.Root("id"),
			"Conflicting Attributes",
			"Only one of id or name may be specified, not both.",
		)
		return
	}

	// If ID is provided, look it up directly.
	if idSet {
		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/dns/zones/"+state.ID.ValueString()), nil)
		if err != nil {
			resp.Diagnostics.AddError("Failed to read DNS zone", err.Error())
			return
		}

		var zone apiDNSZone
		if err := json.Unmarshal(apiResp.Body, &zone); err != nil {
			resp.Diagnostics.AddError("Failed to parse DNS zone response", err.Error())
			return
		}

		d.setZoneState(ctx, &state, &zone, resp)
		return
	}

	// Filter by name. The backend stores names normalized (lowercase, trailing
	// dot), so normalize the configured value, push it as a server-side filter,
	// and match against it — a name written as "Example.com" still resolves.
	wantName := normalizeZoneName(state.Name.ValueString())
	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/dns/zones"), url.Values{"name": {wantName}})
	if err != nil {
		resp.Diagnostics.AddError("Failed to list DNS zones", err.Error())
		return
	}

	var list apiDNSZoneList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse DNS zones response", err.Error())
		return
	}

	var found *apiDNSZone
	for i := range list.Zones {
		if list.Zones[i].Name == wantName {
			found = &list.Zones[i]
			break
		}
	}

	if found == nil {
		resp.Diagnostics.AddError("DNS zone not found", fmt.Sprintf("No DNS zone found with name %q.", wantName))
		return
	}

	d.setZoneState(ctx, &state, found, resp)
}

func (d *dnsZoneDataSource) setZoneState(ctx context.Context, state *dnsZoneModel, zone *apiDNSZone, resp *datasource.ReadResponse) {
	state.ID = types.StringValue(zone.ID)
	state.Name = types.StringValue(zone.Name)
	state.Email = types.StringValue(zone.Email)
	state.Description = types.StringValue(zone.Description)
	state.TTL = types.Int64Value(int64(zone.TTL))
	state.Type = types.StringValue(zone.Type)
	state.Status = types.StringValue(zone.Status)
	state.Serial = types.Int64Value(int64(zone.Serial))
	state.RecordCount = types.Int64Value(int64(zone.RecordCount))
	state.CreatedAt = types.StringValue(zone.CreatedAt)

	nsList, diags := types.ListValueFrom(ctx, types.StringType, zone.NameServers)
	resp.Diagnostics.Append(diags...)
	state.NameServers = nsList

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
