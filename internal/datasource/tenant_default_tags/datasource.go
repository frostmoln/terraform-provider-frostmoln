// Package tenantdefaulttags implements the frostmoln_tenant_default_tags data
// source: a tenant's default tags, read-only.
package tenantdefaulttags

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tagsettings"
)

var _ datasource.DataSource = &tenantDefaultTagsDataSource{}

// NewDataSource returns a new frostmoln_tenant_default_tags data source factory.
func NewDataSource() datasource.DataSource {
	return &tenantDefaultTagsDataSource{}
}

type tenantDefaultTagsDataSource struct {
	client *client.Client
}

type tenantDefaultTagsModel struct {
	TenantID types.String `tfsdk:"tenant_id"`
	Tags     types.Map    `tfsdk:"tags"`
}

func (d *tenantDefaultTagsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant_default_tags"
}

func (d *tenantDefaultTagsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads a tenant's default tags: the tags the platform copies onto every taggable resource " +
			"created in the tenant, whoever creates it. They apply only to resources created after they were set, " +
			"and a resource's own tags win over them. Any member of the organization that owns the tenant can read " +
			"them; an API key needs `organizations:read`. Manage them with the `frostmoln_tenant_default_tags` " +
			"resource.",
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{
				Description: "The tenant to read. Defaults to the provider's tenant.",
				Optional:    true,
				Computed:    true,
				Validators:  []validator.String{tagsettings.TenantIDValidator()},
			},
			"tags": schema.MapAttribute{
				Description: "The tenant's default tags; empty when none are set.",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

func (d *tenantDefaultTagsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *tenantDefaultTagsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg tenantDefaultTagsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := d.client.TenantID()
	if !cfg.TenantID.IsNull() && !cfg.TenantID.IsUnknown() {
		tenantID = cfg.TenantID.ValueString()
	}
	tags, err := tagsettings.GetDefaultTags(ctx, d.client, tenantID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read tenant default tags", err.Error())
		return
	}
	m, diags := types.MapValueFrom(ctx, types.StringType, tags)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &tenantDefaultTagsModel{
		TenantID: types.StringValue(tenantID),
		Tags:     m,
	})...)
}
