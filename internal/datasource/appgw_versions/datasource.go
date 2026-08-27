// Package appgw_versions implements the frostmoln_appgw_versions data source.
package appgw_versions

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ datasource.DataSource              = &versionsDataSource{}
	_ datasource.DataSourceWithConfigure = &versionsDataSource{}
)

// VersionsModel is the data source's state.
type VersionsModel struct {
	LaunchableOnly types.Bool     `tfsdk:"launchable_only"`
	Versions       []VersionModel `tfsdk:"versions"`
	Default        types.String   `tfsdk:"default"`
}

// VersionModel is one data-plane engine version.
type VersionModel struct {
	ID                    types.String `tfsdk:"id"`
	Engine                types.String `tfsdk:"engine"`
	Version               types.String `tfsdk:"version"`
	Status                types.String `tfsdk:"status"`
	IsDefault             types.Bool   `tfsdk:"is_default"`
	Launchable            types.Bool   `tfsdk:"launchable"`
	ManagedRulesetVersion types.String `tfsdk:"managed_ruleset_version_default"`
	EndOfLife             types.String `tfsdk:"end_of_life"`
	ReleaseNotesURL       types.String `tfsdk:"release_notes_url"`
}

type apiVersion struct {
	ID                string  `json:"id"`
	Engine            string  `json:"engine"`
	Version           string  `json:"version"`
	Status            string  `json:"status"`
	IsDefault         bool    `json:"isDefault"`
	CRSVersionDefault string  `json:"crsVersionDefault"`
	Launchable        bool    `json:"launchable"`
	EndOfLife         *string `json:"endOfLife,omitempty"`
	ReleaseNotesURL   string  `json:"releaseNotesUrl,omitempty"`
	Disabled          bool    `json:"disabled"`
}

type apiVersionListResponse struct {
	Versions []apiVersion `json:"versions"`
}

type versionsDataSource struct {
	client *client.Client
}

// NewDataSource returns a new versions data source factory.
func NewDataSource() datasource.DataSource { return &versionsDataSource{} }

func (d *versionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_versions"
}

func (d *versionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Application Gateway data-plane engine versions.\n\n" +
			"Use `launchable` to decide what you may create a gateway on — it is computed by the " +
			"server from the version's lifecycle status. **Do not re-derive it from `status`**: the " +
			"rule is the platform's, and a configuration that reimplements it is one lifecycle " +
			"change away from selecting a version the server refuses.",
		Attributes: map[string]schema.Attribute{
			"launchable_only": schema.BoolAttribute{
				Description: "Return only versions a new gateway may be created on. Defaults to false.",
				Optional:    true,
			},
			"default": schema.StringAttribute{
				Description: "The version a gateway gets when none is named. Empty if the catalog " +
					"has no default.",
				Computed: true,
			},
			"versions": schema.ListNestedAttribute{
				Description: "The engine versions in the catalog.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":      schema.StringAttribute{Description: "The catalog row's id.", Computed: true},
						"engine":  schema.StringAttribute{Description: "The data-plane engine.", Computed: true},
						"version": schema.StringAttribute{Description: "The version string.", Computed: true},
						"status": schema.StringAttribute{
							Description: "The lifecycle status: `innovation`, `current`, `supported`, " +
								"`deprecated` or `eol`.",
							Computed: true,
						},
						"is_default": schema.BoolAttribute{Description: "Whether this is the catalog default.", Computed: true},
						"launchable": schema.BoolAttribute{
							Description: "Whether a new gateway may be created on it. Server-computed — obey it.",
							Computed:    true,
						},
						"managed_ruleset_version_default": schema.StringAttribute{
							Description: "The managed WAF ruleset version this engine version ships with.",
							Computed:    true,
						},
						"end_of_life":       schema.StringAttribute{Description: "When support ends. Empty if not scheduled.", Computed: true},
						"release_notes_url": schema.StringAttribute{Description: "Where to read what changed.", Computed: true},
					},
				},
			},
		},
	}
}

func (d *versionsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *versionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg VersionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := d.client.Get(ctx, "/v1/application-gateways/versions", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Application Gateway Versions", err.Error())
		return
	}
	list, err := client.ParseResponse[apiVersionListResponse](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Versions Response", err.Error())
		return
	}
	launchableOnly := cfg.LaunchableOnly.ValueBool()
	cfg.Versions = make([]VersionModel, 0, len(list.Versions))
	cfg.Default = types.StringValue("")
	for _, v := range list.Versions {
		if v.IsDefault {
			cfg.Default = types.StringValue(v.Version)
		}
		if launchableOnly && !v.Launchable {
			continue
		}
		eol := ""
		if v.EndOfLife != nil {
			eol = *v.EndOfLife
		}
		cfg.Versions = append(cfg.Versions, VersionModel{
			ID:                    types.StringValue(v.ID),
			Engine:                types.StringValue(v.Engine),
			Version:               types.StringValue(v.Version),
			Status:                types.StringValue(v.Status),
			IsDefault:             types.BoolValue(v.IsDefault),
			Launchable:            types.BoolValue(v.Launchable),
			ManagedRulesetVersion: types.StringValue(v.CRSVersionDefault),
			EndOfLife:             types.StringValue(eol),
			ReleaseNotesURL:       types.StringValue(v.ReleaseNotesURL),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
