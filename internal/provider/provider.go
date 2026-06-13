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

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	apacheinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/apache_instance"
	cacheinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/cache_instance"
	databaseenginesds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/database_engines"
	flavords "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/flavor"
	flavorsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/flavors"
	imageds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/image"
	imagesds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/images"
	instanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/instance"
	mysqlversionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/mysql_versions"
	nginxinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/nginx_instance"
	postgresversionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/postgres_versions"
	redisinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/redis_instance"
	regionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/regions"
	secretds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/secret"
	subnetds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/subnet"
	valkeyinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/valkey_instance"
	vpcds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/vpc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/apache_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/api_key"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/bucket"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/cache_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/floating_ip"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/launch_template"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_health_monitor"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_listener"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_member"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_pool"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/load_balancer"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/mysql_backup"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/mysql_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/mysql_read_replica"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/nginx_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/postgres_backup"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/postgres_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/postgres_read_replica"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/redis_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/s3_credential"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/scale_group"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/secret"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/security_group"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/security_group_rule"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/snapshot"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/ssh_key"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/subnet"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/valkey_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/volume"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/volume_attachment"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/vpc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/webserver_domain"
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
		Description: "Terraform provider for the Frostmoln Cloud Platform.",
		Attributes: map[string]schema.Attribute{
			"api_endpoint": schema.StringAttribute{
				Description: "The API endpoint URL. Can also be set via the FROSTMOLN_API_ENDPOINT environment variable. Defaults to https://api.frostmoln.cloud.",
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
	apiEndpoint := "https://api.frostmoln.cloud"
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
	c := client.NewClient(
		apiEndpoint, apiKey,
		client.WithUserAgent("terraform-provider-frostmoln/"+p.version),
	)

	if err := c.Configure(ctx); err != nil {
		resp.Diagnostics.AddError(
			"Failed to Configure Provider",
			"Unable to authenticate with the Frostmoln API: "+err.Error(),
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
		load_balancer.NewResource,
		lb_listener.NewResource,
		lb_pool.NewResource,
		lb_member.NewResource,
		lb_health_monitor.NewResource,
		volume.NewResource,
		volume_attachment.NewResource,
		snapshot.NewResource,
		instance.NewResource,
		launch_template.NewResource,
		postgres_instance.NewResource,
		postgres_backup.NewResource,
		postgres_read_replica.NewResource,
		mysql_instance.NewResource,
		mysql_backup.NewResource,
		mysql_read_replica.NewResource,
		redis_instance.NewResource,
		cache_instance.NewResource,
		valkey_instance.NewResource,
		scale_group.NewResource,
		secret.NewResource,
		api_key.NewResource,
		apache_instance.NewResource,
		nginx_instance.NewResource,
		webserver_domain.NewResource,
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
		cacheinstanceds.NewDataSource,
		valkeyinstanceds.NewDataSource,
		secretds.NewDataSource,
		apacheinstanceds.NewDataSource,
		nginxinstanceds.NewDataSource,
		regionsds.NewDataSource,
	}
}
