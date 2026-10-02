// Package tenantquotas implements the frostmoln_tenant_quotas data source: the
// provider tenant's quota limits and usage, read-only.
package tenantquotas

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &tenantQuotasDataSource{}

// NewDataSource returns a new frostmoln_tenant_quotas data source factory.
func NewDataSource() datasource.DataSource {
	return &tenantQuotasDataSource{}
}

type tenantQuotasDataSource struct {
	client *client.Client
}

type tenantQuotasModel struct {
	Quotas types.Map `tfsdk:"quotas"`
}

type quotaModel struct {
	Limit types.Int64  `tfsdk:"limit"`
	Used  types.Int64  `tfsdk:"used"`
	Unit  types.String `tfsdk:"unit"`
}

var quotaAttrTypes = map[string]attr.Type{
	"limit": types.Int64Type,
	"used":  types.Int64Type,
	"unit":  types.StringType,
}

// apiQuotasReport is provisioning's tenant quota report. Fields the provider
// does not surface (allocated, requestable, ...) are ignored.
type apiQuotasReport struct {
	Report struct {
		Quotas []struct {
			ResourceType string `json:"resourceType"`
			Used         *int64 `json:"used"`
			Limit        int64  `json:"limit"`
			Unit         string `json:"unit"`
		} `json:"quotas"`
	} `json:"report"`
}

// apiComputeImageQuota is the custom-image slice of compute's /quotas and
// /quotas/usage bodies (same shape for both).
type apiComputeImageQuota struct {
	Images         int64 `json:"images"`
	ImageStorageGB int64 `json:"imageStorageGb"`
}

func (d *tenantQuotasDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant_quotas"
}

func (d *tenantQuotasDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the quota limits and current usage of the provider's tenant, keyed by resource type " +
			"(e.g. `vcpu`, `ram`, `volume`, `registry_storage`, `image`, `image_storage`). A limit of 0 means " +
			"no allowance; there is no \"unlimited\" value. Each value is in its counter's own unit. The set of " +
			"resource types is defined by the server, so new keys can appear without a provider release. Only the " +
			"tenant view is available: the organization-wide report is not served to API keys. To raise a limit, " +
			"request an increase on the portal's Quotas page or with `fm tenant quota request`. The `image` and " +
			"`image_storage` keys are always present (limit 0 when the tenant has no custom-image allowance). The read " +
			"fails if either the provisioning or the compute quota endpoint is unavailable.",
		Attributes: map[string]schema.Attribute{
			"quotas": schema.MapNestedAttribute{
				Description: "The tenant's quotas, keyed by resource type.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"limit": schema.Int64Attribute{
							Description: "The tenant's limit for this resource type, in `unit`. 0 means no allowance.",
							Computed:    true,
						},
						"used": schema.Int64Attribute{
							Description: "Current usage, in `unit`. Null when the platform cannot currently report usage " +
								"for this resource type; null means unknown and is never zero.",
							Computed: true,
						},
						"unit": schema.StringAttribute{
							Description: "The counter's unit: `count`, `gb` (gigabytes as the platform reports them; block-storage " +
								"sizes follow the volume size unit) or `mb` (megabytes; only `ram`, where 1 GB = 1024 MB).",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

func (d *tenantQuotasDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func getJSON(ctx context.Context, c *client.Client, path string, out any) error {
	r, err := c.Get(ctx, path, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func (d *tenantQuotasDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	// Every call must succeed: Terraform has no partial-success channel, and a
	// silently missing key would break a `quotas["image"]` lookup downstream.
	var report apiQuotasReport
	if err := getJSON(ctx, d.client, d.client.TenantPath("/quotas-report"), &report); err != nil {
		resp.Diagnostics.AddError("Failed to read tenant quotas", err.Error())
		return
	}
	var imgLimit, imgUsage apiComputeImageQuota
	if err := getJSON(ctx, d.client, d.client.TenantPath("/quotas"), &imgLimit); err != nil {
		resp.Diagnostics.AddError("Failed to read tenant image quotas", err.Error())
		return
	}
	if err := getJSON(ctx, d.client, d.client.TenantPath("/quotas/usage"), &imgUsage); err != nil {
		resp.Diagnostics.AddError("Failed to read tenant image usage", err.Error())
		return
	}

	quotas := make(map[string]quotaModel, len(report.Report.Quotas)+2)
	for _, q := range report.Report.Quotas {
		used := types.Int64Null() // nil is unknown usage, never 0
		if q.Used != nil {
			used = types.Int64Value(*q.Used)
		}
		quotas[q.ResourceType] = quotaModel{Limit: types.Int64Value(q.Limit), Used: used, Unit: types.StringValue(q.Unit)}
	}
	// Always emitted, unlike the portal and fm, which hide them at limit 0: those
	// are displays, but here a missing key fails `quotas["image"]` at plan time.
	quotas["image"] = quotaModel{Limit: types.Int64Value(imgLimit.Images), Used: types.Int64Value(imgUsage.Images), Unit: types.StringValue("count")}
	quotas["image_storage"] = quotaModel{Limit: types.Int64Value(imgLimit.ImageStorageGB), Used: types.Int64Value(imgUsage.ImageStorageGB), Unit: types.StringValue("gb")}

	m, diags := types.MapValueFrom(ctx, types.ObjectType{AttrTypes: quotaAttrTypes}, quotas)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &tenantQuotasModel{Quotas: m})...)
}
