// Package provider implements the Frostmoln Terraform provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/oidc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/clicreds"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	apacheinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/apache_instance"
	databaseenginesds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/database_engines"
	dnszoneds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/dns_zone"
	flavords "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/flavor"
	flavorsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/flavors"
	imageds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/image"
	imagesds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/images"
	instanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/instance"
	kubernetesaddonsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/kubernetes_addons"
	kubernetesflavorsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/kubernetes_flavors"
	kubernetestiersds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/kubernetes_tiers"
	kubernetesversionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/kubernetes_versions"
	messaginginstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/messaging_instance"
	mysqlversionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/mysql_versions"
	nginxinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/nginx_instance"
	postgresversionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/postgres_versions"
	redisinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/redis_instance"
	regionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/regions"
	secretds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/secret"
	subnetds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/subnet"
	valkeyinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/valkey_instance"
	volumetiersds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/volume_tiers"
	vpcds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/vpc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/apache_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/api_key"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/bucket"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/dns_record"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/dns_zone"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/floating_ip"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/instance_port_security_groups"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/kubernetes_cluster"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/launch_template"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_health_monitor"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_listener"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_member"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/lb_pool"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/load_balancer"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/messaging_instance"
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
	APIEndpoint   types.String `tfsdk:"api_endpoint"`
	APIKey        types.String `tfsdk:"api_key"`
	TenantID      types.String `tfsdk:"tenant_id"`
	UseCLIConfig  types.Bool   `tfsdk:"use_cli_config"`
	CLIConfigPath types.String `tfsdk:"cli_config_path"`
	CLIContext    types.String `tfsdk:"cli_context"`
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
				Description: "The API key for authentication. Can also be set via the FROSTMOLN_API_KEY environment variable. " +
					"When unset, the provider falls back to an existing fm CLI session (see use_cli_config).",
				Optional:  true,
				Sensitive: true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant to manage resources in. Defaults to your account's default tenant. " +
					"Targeting another tenant requires an fm CLI / OIDC session whose user belongs to multiple tenants; " +
					"an API key is bound to a single tenant. One tenant per provider instance — use a second provider with " +
					"an alias to span tenants. Can also be set via the FROSTMOLN_TENANT_ID environment variable.",
				Optional: true,
			},
			"use_cli_config": schema.BoolAttribute{
				Description: "When no api_key is configured, fall back to the credentials in the fm CLI config " +
					"(~/.fm/config.yaml): its stored API key, or its OIDC session (with automatic token refresh). " +
					"Defaults to true. Can also be set via FROSTMOLN_USE_CLI_CONFIG. Set to false in CI to require an explicit api_key.",
				Optional: true,
			},
			"cli_config_path": schema.StringAttribute{
				Description: "Path to the fm CLI config file. Defaults to ~/.fm/config.yaml. " +
					"Can also be set via the FROSTMOLN_CLI_CONFIG environment variable.",
				Optional: true,
			},
			"cli_context": schema.StringAttribute{
				Description: "Name of the fm CLI context to read credentials from. Defaults to the config file's current_context.",
				Optional:    true,
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

	// Resolve API endpoint. Track whether it was set explicitly: when it was
	// not and the credential comes from the CLI config, we adopt that config's
	// endpoint (which includes the /api suffix the CLI stores).
	apiEndpoint := defaultAPIEndpoint
	endpointExplicit := false
	if !config.APIEndpoint.IsNull() && !config.APIEndpoint.IsUnknown() {
		apiEndpoint = config.APIEndpoint.ValueString()
		endpointExplicit = true
	} else if v := os.Getenv("FROSTMOLN_API_ENDPOINT"); v != "" {
		apiEndpoint = v
		endpointExplicit = true
	}

	userAgent := "terraform-provider-frostmoln/" + p.version
	ua := client.WithUserAgent(userAgent)
	// Stamp the provider build version so the gateway can enforce a minimum
	// supported version (X-FM-Provider-Version, ADR-0088).
	ver := client.WithClientVersion(p.version)
	// Select the operating tenant (tenant_id attr > FROSTMOLN_TENANT_ID > the
	// /v1/me default). A no-op when empty; the gateway authorizes the selection.
	tenantOpt := client.WithTenantID(resolveTenantID(config))

	useCLI, err := resolveUseCLIConfig(config)
	if err != nil {
		resp.Diagnostics.AddError("Invalid use_cli_config", err.Error())
		return
	}

	// Credential resolution order: explicit api_key attr > FROSTMOLN_API_KEY >
	// fm CLI config (api_key, else OIDC bearer with refresh) > error.
	var c *client.Client
	cliConfigFound := false

	apiKey := ""
	if !config.APIKey.IsNull() && !config.APIKey.IsUnknown() {
		apiKey = config.APIKey.ValueString()
	} else if v := os.Getenv("FROSTMOLN_API_KEY"); v != "" {
		apiKey = v
	}

	switch {
	case apiKey != "":
		c = client.NewClient(apiEndpoint, apiKey, ua, ver, tenantOpt)

	case useCLI:
		resolved, rerr := clicreds.Resolve(clicreds.Options{
			Path:      cliConfigPath(config),
			Context:   stringValue(config.CLIContext),
			UserAgent: userAgent,
		})
		switch {
		case errors.Is(rerr, clicreds.ErrNotFound):
			// No CLI config — fall through to the missing-credentials error.
		case rerr != nil:
			resp.Diagnostics.AddError("Invalid fm CLI configuration", rerr.Error())
			return
		default:
			cliConfigFound = true
			if resolved.PermsWarning != "" {
				resp.Diagnostics.AddWarning("Insecure fm CLI config permissions", resolved.PermsWarning)
			}
			// Adopt the CLI's endpoint (with /api) unless one was set explicitly.
			endpoint := chooseCLIEndpoint(endpointExplicit, apiEndpoint, resolved.APIEndpoint)
			switch {
			case resolved.APIKey != "":
				c = client.NewClient(endpoint, resolved.APIKey, ua, ver, tenantOpt)
			case resolved.AccessToken != "":
				// The OIDC bearer token (and the refresh token it is exchanged
				// with) must only travel over https; refuse an insecure endpoint.
				if !oidc.SecureURL(endpoint) {
					resp.Diagnostics.AddError(
						"Insecure API endpoint for fm CLI session",
						fmt.Sprintf("the fm CLI session authenticates with an OIDC bearer token, which must be sent over https; refusing endpoint %q", endpoint),
					)
					return
				}
				c = client.NewClient(endpoint, "", ua, ver, tenantOpt, client.WithTokenSource(client.TokenSourceConfig{
					AccessToken:  resolved.AccessToken,
					RefreshToken: resolved.RefreshToken,
					ExpiresAt:    resolved.ExpiresAt,
					Source:       resolved.Bearer,
				}))
			}
		}
	}

	if c == nil {
		if cliConfigFound {
			resp.Diagnostics.AddError(
				"Missing Credentials",
				"An fm CLI config was found but the selected context has no usable credentials. "+
					"Run `fm auth login`, or set the `api_key` attribute or FROSTMOLN_API_KEY environment variable.",
			)
		} else {
			resp.Diagnostics.AddError(
				"Missing Credentials",
				"No Frostmoln credentials found. Provide one of:\n"+
					"  - the `api_key` provider attribute\n"+
					"  - the FROSTMOLN_API_KEY environment variable\n"+
					"  - an fm CLI session: run `fm auth login` (reads ~/.fm/config.yaml; disabled when use_cli_config = false)",
			)
		}
		return
	}

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

const (
	// defaultAPIEndpoint is the api-key path default (historical, no /api suffix).
	defaultAPIEndpoint = "https://api.frostmoln.cloud"
	// defaultCLIAPIEndpoint is the fm-CLI-session default. The gateway mounts
	// customer routes under /api/*, so the CLI path must use the /api form.
	defaultCLIAPIEndpoint = "https://api.frostmoln.cloud/api"
)

// resolveUseCLIConfig reports whether the fm CLI config fallback is enabled.
// Defaults to true; the use_cli_config attribute wins, else
// FROSTMOLN_USE_CLI_CONFIG. An unparseable env value is an error rather than a
// silent fall-back-to-enabled, since the flag's job is to disable the home-dir
// read in CI.
func resolveUseCLIConfig(config FrostmolnProviderModel) (bool, error) {
	if !config.UseCLIConfig.IsNull() && !config.UseCLIConfig.IsUnknown() {
		return config.UseCLIConfig.ValueBool(), nil
	}
	if v := os.Getenv("FROSTMOLN_USE_CLI_CONFIG"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("FROSTMOLN_USE_CLI_CONFIG=%q is not a valid boolean (use true or false)", v)
		}
		return b, nil
	}
	return true, nil
}

// resolveTenantID picks the operating tenant: the tenant_id attribute wins,
// else FROSTMOLN_TENANT_ID, else "" (the client adopts the /v1/me default).
func resolveTenantID(config FrostmolnProviderModel) string {
	if v := stringValue(config.TenantID); v != "" {
		return v
	}
	return os.Getenv("FROSTMOLN_TENANT_ID")
}

// chooseCLIEndpoint picks the API endpoint for a CLI-sourced credential: an
// explicitly-set endpoint wins; otherwise adopt the CLI config's endpoint
// (which carries the /api suffix), falling back to the /api default rather than
// the bare api-key default — the bare form 404s at the edge.
func chooseCLIEndpoint(explicit bool, base, cliEndpoint string) string {
	if explicit {
		return base
	}
	if cliEndpoint != "" {
		return cliEndpoint
	}
	return defaultCLIAPIEndpoint
}

// cliConfigPath resolves the fm CLI config path override: the cli_config_path
// attribute, else FROSTMOLN_CLI_CONFIG. "" lets clicreds use the default
// ~/.fm/config.yaml.
func cliConfigPath(config FrostmolnProviderModel) string {
	if v := stringValue(config.CLIConfigPath); v != "" {
		return v
	}
	return os.Getenv("FROSTMOLN_CLI_CONFIG")
}

// stringValue returns the attribute's value, or "" when null/unknown.
func stringValue(s types.String) string {
	if s.IsNull() || s.IsUnknown() {
		return ""
	}
	return s.ValueString()
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
		dns_zone.NewResource,
		dns_record.NewResource,
		load_balancer.NewResource,
		lb_listener.NewResource,
		lb_pool.NewResource,
		lb_member.NewResource,
		lb_health_monitor.NewResource,
		volume.NewResource,
		volume_attachment.NewResource,
		snapshot.NewResource,
		instance.NewResource,
		instance_port_security_groups.NewResource,
		kubernetes_cluster.NewResource,
		launch_template.NewResource,
		postgres_instance.NewResource,
		postgres_backup.NewResource,
		postgres_read_replica.NewResource,
		mysql_instance.NewResource,
		mysql_backup.NewResource,
		mysql_read_replica.NewResource,
		redis_instance.NewResource,
		valkey_instance.NewResource,
		messaging_instance.NewResource,
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
		dnszoneds.NewDataSource,
		instanceds.NewDataSource,
		postgresversionsds.NewDataSource,
		mysqlversionsds.NewDataSource,
		databaseenginesds.NewDataSource,
		redisinstanceds.NewDataSource,
		valkeyinstanceds.NewDataSource,
		messaginginstanceds.NewDataSource,
		secretds.NewDataSource,
		apacheinstanceds.NewDataSource,
		nginxinstanceds.NewDataSource,
		regionsds.NewDataSource,
		volumetiersds.NewDataSource,
		kubernetesversionsds.NewDataSource,
		kubernetestiersds.NewDataSource,
		kubernetesflavorsds.NewDataSource,
		kubernetesaddonsds.NewDataSource,
	}
}
