// Package provider implements the Frostmoln Terraform provider.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/client"
	databaseenginesds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/database_engines"
	flavords "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/flavor"
	flavorsds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/flavors"
	imageds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/image"
	imagesds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/images"
	instanceds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/instance"
	mysqlversionsds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/mysql_versions"
	postgresversionsds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/postgres_versions"
	redisinstanceds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/redis_instance"
	subnetds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/subnet"
	vpcds "git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/datasource/vpc"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/api_key"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/bucket"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/floating_ip"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/instance"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/mysql_backup"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/mysql_instance"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/mysql_read_replica"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/postgres_backup"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/postgres_instance"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/postgres_read_replica"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/redis_instance"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/s3_credential"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/security_group"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/security_group_rule"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/snapshot"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/ssh_key"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/subnet"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/volume"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/volume_attachment"
	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/resource/vpc"
)

var _ provider.Provider = &FrostmolnProvider{}

// FrostmolnProvider implements the Frostmoln Terraform provider.
type FrostmolnProvider struct {
	version string
}

// FrostmolnProviderModel describes the provider data model.
type FrostmolnProviderModel struct {
	APIEndpoint types.String `tfsdk:"api_endpoint"`
	APIKey      types.String `tfsdk:"api_key"`
}

// New creates a new provider factory function.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &FrostmolnProvider{
			version: version,
		}
	}
}

func (p *FrostmolnProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "frostmoln"
	resp.Version = p.version
}

func (p *FrostmolnProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Terraform provider for the NordicLight (Frostmoln) Cloud Platform.",
		Attributes: map[string]schema.Attribute{
			"api_endpoint": schema.StringAttribute{
				Description: "The API endpoint URL. Can also be set via the FROSTMOLN_API_ENDPOINT environment variable. Defaults to https://api.nordiclight.cloud.",
				Optional:    true,
			},
			"api_key": schema.StringAttribute{
				Description: "The API key for authentication. Can also be set via the FROSTMOLN_API_KEY environment variable.",
				Optional:    true,
				Sensitive:   true,
			},
		},
	}
}

func (p *FrostmolnProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config FrostmolnProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve API endpoint
	apiEndpoint := "https://api.nordiclight.cloud"
	if !config.APIEndpoint.IsNull() && !config.APIEndpoint.IsUnknown() {
		apiEndpoint = config.APIEndpoint.ValueString()
	} else if v := os.Getenv("FROSTMOLN_API_ENDPOINT"); v != "" {
		apiEndpoint = v
	}

	// Resolve API key
	apiKey := ""
	if !config.APIKey.IsNull() && !config.APIKey.IsUnknown() {
		apiKey = config.APIKey.ValueString()
	} else if v := os.Getenv("FROSTMOLN_API_KEY"); v != "" {
		apiKey = v
	}

	if apiKey == "" {
		resp.Diagnostics.AddError(
			"Missing API Key",
			"The provider requires an API key. Set it via the api_key attribute or the FROSTMOLN_API_KEY environment variable.",
		)
		return
	}

	// Create and configure client
	c := client.NewClient(apiEndpoint, apiKey,
		client.WithUserAgent("terraform-provider-frostmoln/"+p.version),
	)

	if err := c.Configure(ctx); err != nil {
		resp.Diagnostics.AddError(
			"Failed to Configure Provider",
			"Unable to authenticate with the NordicLight API: "+err.Error(),
		)
		return
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *FrostmolnProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		ssh_key.NewResource,
		bucket.NewResource,
		s3_credential.NewResource,
		vpc.NewResource,
		subnet.NewResource,
		security_group.NewResource,
		security_group_rule.NewResource,
		floating_ip.NewResource,
		volume.NewResource,
		volume_attachment.NewResource,
		snapshot.NewResource,
		instance.NewResource,
		postgres_instance.NewResource,
		postgres_backup.NewResource,
		postgres_read_replica.NewResource,
		mysql_instance.NewResource,
		mysql_backup.NewResource,
		mysql_read_replica.NewResource,
		redis_instance.NewResource,
		api_key.NewResource,
	}
}

func (p *FrostmolnProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		imageds.NewDataSource,
		imagesds.NewDataSource,
		flavords.NewDataSource,
		flavorsds.NewDataSource,
		vpcds.NewDataSource,
		subnetds.NewDataSource,
		instanceds.NewDataSource,
		postgresversionsds.NewDataSource,
		mysqlversionsds.NewDataSource,
		databaseenginesds.NewDataSource,
		redisinstanceds.NewDataSource,
	}
}
