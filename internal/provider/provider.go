// Package provider implements the Frostmoln Terraform provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"go.frostmoln.internal/oidc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/clicreds"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	apacheinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/apache_instance"
	apikeyscopesds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/api_key_scopes"
	appgwflavorsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/appgw_flavors"
	appgwwafrulesds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/appgw_waf_rules"
	dscontainerregistry "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/container_registry"
	databaseenginesds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/database_engines"
	dnszoneds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/dns_zone"
	flavords "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/flavor"
	flavorsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/flavors"
	gatewayds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/gateway"
	iampolicydocumentds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/iam_policy_document"
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
	publicipds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/public_ip"
	redisinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/redis_instance"
	regionsds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/regions"
	secretds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/secret"
	subnetds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/subnet"
	valkeyinstanceds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/valkey_instance"
	volumetiersds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/volume_tiers"
	vpcds "go.frostmoln.internal/terraform-provider-frostmoln/internal/datasource/vpc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/apache_instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/api_key"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_backend"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_backend_authorization"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_backend_pool"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_certificate"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_config_apply"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_health_check"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_listener"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_route"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_waf_exclusion"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_waf_policy"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_waf_policy_publication"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/appgw_waf_rule"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/application_gateway"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/bucket"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/bucket_cors_configuration"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/bucket_lifecycle_configuration"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/container_registry"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/container_registry_credential"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/dns_record"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/dns_zone"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/gateway"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/iam_policy"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/iam_policy_attachment"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/image"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/instance"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/instance_port_security_groups"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/kubernetes_cluster"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/kubernetes_node_pool"
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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/public_ip"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/public_ip_association"
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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/vpc_route"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/webserver_deployment"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/webserver_domain"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/workload_identity_binding"
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
				Description: "The API endpoint URL, including the /api prefix the gateway mounts customer routes under. " +
					"Can also be set via the FROSTMOLN_API_ENDPOINT environment variable. Defaults to https://api.frostmoln.cloud/api.",
				Optional: true,
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
	// Whether the endpoint in use was adopted from the fm CLI config, which
	// changes where a wrong endpoint has to be fixed (see endpointHint).
	endpointFromCLIConfig := false

	// An attribute set to "" counts as UNSET, so an empty attribute bound from a
	// variable with an empty default falls through to the environment instead of
	// silently disabling it and landing on the CLI session.
	apiKey := ""
	apiKeyFromAttribute := false // pragma: allowlist secret
	if v := stringValue(config.APIKey); v != "" {
		apiKey, apiKeyFromAttribute = v, true // pragma: allowlist secret
	} else if v := os.Getenv("FROSTMOLN_API_KEY"); v != "" {
		apiKey = v // pragma: allowlist secret
	}

	// Which credential is in play, named at the point it is chosen. The
	// resolution order silently falls through — api_key, then
	// FROSTMOLN_API_KEY, then whatever `fm auth login` left in
	// ~/.fm/config.yaml — so a practitioner who believes they are running as
	// their API key can be running as their own user instead, and nothing said
	// so. A customer created an API key for CLI and Terraform, applied a
	// configuration, and reported the key as "never used": it was never used,
	// because their shell had no FROSTMOLN_API_KEY and the provider quietly
	// used their CLI session. INFO, so `TF_LOG=INFO` answers the question and a
	// normal run stays quiet.
	credentialSource, credentialKind := "", ""

	switch {
	case apiKey != "":
		credentialSource, credentialKind = "the FROSTMOLN_API_KEY environment variable", "api_key_env"
		if apiKeyFromAttribute {
			credentialSource, credentialKind = "the api_key provider attribute", "api_key_attribute"
		}
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
			endpointFromCLIConfig = !endpointExplicit && resolved.APIEndpoint != ""
			switch {
			case resolved.APIKey != "":
				credentialSource = fmt.Sprintf("the API key stored in the fm CLI config (%s)", cliSourceLabel(resolved.Path, resolved.Context))
				credentialKind = "cli_api_key"
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
				credentialSource = fmt.Sprintf("the fm CLI login session (%s), NOT an API key", cliSourceLabel(resolved.Path, resolved.Context))
				credentialKind = "cli_session"
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

	tflog.Info(ctx, "Frostmoln provider credentials resolved", map[string]any{
		"credential_kind":   credentialKind,
		"credential_source": credentialSource,
		// REDACTED, not raw: the endpoint can carry basic-auth userinfo or a
		// query token (that is why displayURL exists), Terraform does not redact
		// provider logs, and TF_LOG_PROVIDER=INFO is exactly what a practitioner
		// turns on to read this line.
		"api_endpoint": redactedEndpoint(c.Endpoint()),
	})

	if err := c.Configure(ctx); err != nil {
		resp.Diagnostics.AddError(
			"Failed to Configure Provider",
			"Unable to authenticate with the Frostmoln API using "+credentialSource+": "+
				err.Error()+endpointHint(c.Endpoint(), endpointFromCLIConfig, err),
		)
		return
	}

	// WHO, not just which source. The customer's question was "was my API key
	// used?", and the answer they needed was that every resource was being
	// created as their own user. Configure has just resolved both from
	// GET /v1/me, so say it.
	tflog.Info(ctx, "Frostmoln provider authenticated", map[string]any{
		"credential_kind": credentialKind,
		"user_id":         c.UserID(),
		"tenant_id":       c.TenantID(),
	})

	resp.DataSourceData = c
	resp.ResourceData = c
}

// defaultAPIEndpoint is the endpoint used when neither api_endpoint nor
// FROSTMOLN_API_ENDPOINT is set. The gateway mounts customer routes under
// /api/*, so the suffix is mandatory: the bare https://api.frostmoln.cloud form
// 404s at the edge ("NOT_FOUND: the requested resource was not found") on the
// GET /v1/me the provider makes while configuring. It is the same endpoint the
// fm CLI stores in ~/.fm/config.yaml, so both credential paths agree.
const defaultAPIEndpoint = "https://api.frostmoln.cloud/api"

// endpointHint appends a targeted hint when configuration failed with a 404
// against an endpoint that lacks the /api prefix the gateway mounts customer
// routes under. Nothing is served on the bare host, so the edge answers the
// configure-time GET /v1/me with "NOT_FOUND: the requested resource was not
// found" — which reads like a missing account rather than a wrong URL. The
// endpoint is never rewritten: a customer may legitimately front the API with
// their own proxy, so this only names what is wrong.
func endpointHint(endpoint string, fromCLIConfig bool, err error) string {
	if !isNotFound(err) || hasAPIPathPrefix(endpoint) {
		return ""
	}
	u, perr := url.Parse(endpoint)
	if perr != nil {
		return ""
	}
	// Append the prefix to the PATH via net/url rather than concatenating onto
	// the raw string: "https://host?x=1"+"/api" would bury the segment inside
	// the query, and a sub-path-mounted proxy needs it after its own path.
	suggested := *u
	suggested.Path = strings.TrimSuffix(u.Path, "/") + "/api"
	suggested.RawPath = ""

	// A CLI-sourced endpoint was adopted from ~/.fm/config.yaml, so the fix
	// belongs there — telling that practitioner to set api_endpoint would paper
	// over a stale config that keeps breaking `fm` itself.
	remedy := fmt.Sprintf("Set api_endpoint (or FROSTMOLN_API_ENDPOINT) to %q, or leave it unset to use the default (%s).",
		displayURL(&suggested), defaultAPIEndpoint)
	if fromCLIConfig {
		remedy = fmt.Sprintf("It was adopted from the fm CLI config; fix it there with `fm config set api_endpoint %s` "+
			"(a config written before the CLI stored the suffix keeps the old value).", displayURL(&suggested))
	}
	return fmt.Sprintf(
		"\n\nThe endpoint %q has no /api path prefix. Frostmoln's public gateway mounts customer routes under /api, "+
			"so if this endpoint points at it, requests to the bare host 404 at the edge. %s",
		displayURL(u), remedy,
	)
}

// isNotFound reports whether err is a 404 from either credential path. The
// X-API-Key path surfaces the gateway's envelope as *client.APIError, but the
// OIDC bearer path can fail earlier — during the token refresh against
// <endpoint>/v1/auth/cli-config — where the shared oidc module returns a plain
// formatted error ("... : HTTP 404: ..."), not an APIError. That refresh is the
// case most likely to be hitting a stale bare endpoint, so it must not be the
// one case the hint stays silent for.
func isNotFound(err error) bool {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

// displayURL renders an endpoint for a Terraform diagnostic. Terraform does not
// redact provider diagnostics and this one repeats on every plan until the
// endpoint is fixed, so anything credential-shaped in the URL is stripped
// first: the query and fragment are dropped outright, and Redacted() masks a
// userinfo password: a practitioner fronting the API with a basic-auth proxy
// has a working endpoint of the form https://<user>:<password>@host, which
// net/http turns into an Authorization header. Neither part is relevant to the
// /api prefix the hint is about.
func displayURL(u *url.URL) string {
	shown := *u
	shown.RawQuery = ""
	shown.Fragment = ""
	shown.RawFragment = ""
	return shown.Redacted()
}

// hasAPIPathPrefix reports whether the endpoint's path starts with the /api
// segment. An unparseable endpoint counts as "has it" so the hint stays silent
// rather than guessing at a URL it could not read.
func hasAPIPathPrefix(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return true
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
	return first == "api"
}

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
// (which carries the /api suffix), falling back to the default.
func chooseCLIEndpoint(explicit bool, base, cliEndpoint string) string {
	if explicit {
		return base
	}
	if cliEndpoint != "" {
		return cliEndpoint
	}
	return defaultAPIEndpoint
}

// cliSourceLabel names WHICH fm CLI credential was adopted, so the log line and
// the failure diagnostic point at a file the practitioner can open.
func cliSourceLabel(path, contextName string) string {
	if path == "" {
		path = "the fm CLI config"
	}
	if contextName == "" {
		return path
	}
	return path + ", context " + contextName
}

// redactedEndpoint renders an endpoint for a LOG line. Same reasoning as
// displayURL, which redacts it for a diagnostic: the value is practitioner-
// supplied and can carry basic-auth userinfo or a query token, and a log is a
// wider surface than a diagnostic, not a narrower one.
func redactedEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "(unparseable)"
	}
	return displayURL(u)
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
		bucket_cors_configuration.NewResource,
		bucket_lifecycle_configuration.NewResource,
		s3_credential.NewResource,
		container_registry.NewResource,
		container_registry_credential.NewResource,
		vpc.NewResource,
		vpc_route.NewResource,
		subnet.NewResource,
		security_group.NewResource,
		security_group_rule.NewResource,
		public_ip.NewResource,
		public_ip_association.NewResource,
		dns_zone.NewResource,
		gateway.NewResource,
		dns_record.NewResource,
		load_balancer.NewResource,
		lb_listener.NewResource,

		// Application Gateway. Ordered parent-first so the family reads as the
		// object graph it is: a gateway, its listeners and their routes, its
		// pools and their backends, and the WAF policy over all of it.
		appgw_config_apply.NewResource,
		application_gateway.NewResource,
		appgw_listener.NewResource,
		appgw_route.NewResource,
		appgw_backend_pool.NewResource,
		appgw_backend.NewResource,
		appgw_backend_authorization.NewResource,
		appgw_health_check.NewResource,
		appgw_certificate.NewResource,
		appgw_waf_policy.NewResource,
		appgw_waf_rule.NewResource,
		appgw_waf_exclusion.NewResource,
		appgw_waf_policy_publication.NewResource,
		lb_pool.NewResource,
		lb_member.NewResource,
		lb_health_monitor.NewResource,
		volume.NewResource,
		volume_attachment.NewResource,
		snapshot.NewResource,
		image.NewResource,
		instance.NewResource,
		instance_port_security_groups.NewResource,
		kubernetes_cluster.NewResource,
		kubernetes_node_pool.NewResource,
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
		iam_policy.NewResource,
		iam_policy_attachment.NewResource,
		apache_instance.NewResource,
		nginx_instance.NewResource,
		webserver_domain.NewResource,
		webserver_deployment.NewResource,
		workload_identity_binding.NewResource,
	}
}

func (p *FrostmolnProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		imageds.NewDataSource,
		// Read-only: the RESOURCE of the same name opts the tenant in and starts
		// billable storage, so a config that only consumes a registry someone
		// else owns needs a path that cannot create one.
		dscontainerregistry.NewDataSource,
		appgwflavorsds.NewDataSource,
		// Two sources rather than one with an owner argument: a tenant's rules
		// and the platform's are managed by entirely different means, and a
		// forgotten filter argument would silently iterate the wrong set.
		appgwwafrulesds.NewTenantDataSource,
		appgwwafrulesds.NewPlatformDataSource,
		imagesds.NewDataSource,
		flavords.NewDataSource,
		flavorsds.NewDataSource,
		vpcds.NewDataSource,
		subnetds.NewDataSource,
		dnszoneds.NewDataSource,
		gatewayds.NewDataSource,
		publicipds.NewDataSource,
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
		apikeyscopesds.NewDataSource,
		kubernetesversionsds.NewDataSource,
		kubernetestiersds.NewDataSource,
		kubernetesflavorsds.NewDataSource,
		kubernetesaddonsds.NewDataSource,
		iampolicydocumentds.NewDataSource,
	}
}
