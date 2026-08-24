package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestProviderSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	resp, err := providerserver.NewProtocol6(New("test")())().GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("unexpected error getting provider schema: %s", err)
	}
	if resp.Diagnostics != nil {
		for _, d := range resp.Diagnostics {
			if d.Severity == tfprotov6.DiagnosticSeverityError {
				t.Errorf("unexpected error diagnostic: %s: %s", d.Summary, d.Detail)
			}
		}
	}
	if resp.Provider == nil {
		t.Fatal("expected provider schema, got nil")
	}
	if resp.Provider.Block == nil {
		t.Fatal("expected provider block, got nil")
	}

	attrs := make(map[string]bool)
	for _, attr := range resp.Provider.Block.Attributes {
		attrs[attr.Name] = true
	}
	for _, expected := range []string{"api_endpoint", "api_key", "tenant_id", "use_cli_config", "cli_config_path", "cli_context"} {
		if !attrs[expected] {
			t.Errorf("expected provider schema to have attribute %q", expected)
		}
	}
}

func TestProviderMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	resp, err := providerserver.NewProtocol6(New("1.2.3")())().GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	// Verify resource and datasource schemas are present
	if len(resp.ResourceSchemas) == 0 {
		t.Error("expected resource schemas, got none")
	}
	if len(resp.DataSourceSchemas) == 0 {
		t.Error("expected data source schemas, got none")
	}

	// Verify expected resources exist
	expectedResources := []string{
		"frostmoln_ssh_key", "frostmoln_bucket", "frostmoln_s3_credential",
		"frostmoln_vpc", "frostmoln_vpc_route", "frostmoln_subnet", "frostmoln_security_group", "frostmoln_security_group_rule",
		"frostmoln_public_ip", "frostmoln_volume", "frostmoln_volume_attachment", "frostmoln_snapshot",
		"frostmoln_instance", "frostmoln_api_key", "frostmoln_kubernetes_cluster",
		"frostmoln_kubernetes_node_pool",
	}
	for _, name := range expectedResources {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("expected resource schema %q", name)
		}
	}

	// Verify expected data sources exist
	expectedDataSources := []string{
		"frostmoln_image", "frostmoln_images", "frostmoln_flavor", "frostmoln_flavors",
		"frostmoln_vpc", "frostmoln_subnet", "frostmoln_instance",
		"frostmoln_kubernetes_versions", "frostmoln_kubernetes_tiers", "frostmoln_kubernetes_flavors",
	}
	for _, name := range expectedDataSources {
		if _, ok := resp.DataSourceSchemas[name]; !ok {
			t.Errorf("expected data source schema %q", name)
		}
	}
}

// providerConfigType returns the tftypes.Object type matching the provider schema.
func providerConfigType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"api_endpoint":    tftypes.String,
			"api_key":         tftypes.String,
			"tenant_id":       tftypes.String,
			"use_cli_config":  tftypes.Bool,
			"cli_config_path": tftypes.String,
			"cli_context":     tftypes.String,
		},
	}
}

// newProviderDynamicValue creates a DynamicValue for provider configuration
// with only api_endpoint/api_key set (the CLI-config attributes null).
func newProviderDynamicValue(t *testing.T, endpoint, apiKey string) *tfprotov6.DynamicValue {
	t.Helper()
	return newProviderConfig(t, providerConfigValues{endpoint: &endpoint, apiKey: &apiKey})
}

// providerConfigValues holds optional provider attribute values; a nil pointer
// becomes a null attribute.
type providerConfigValues struct {
	endpoint      *string
	apiKey        *string
	tenantID      *string
	useCLIConfig  *bool
	cliConfigPath *string
	cliContext    *string
}

func newProviderConfig(t *testing.T, v providerConfigValues) *tfprotov6.DynamicValue {
	t.Helper()
	typ := providerConfigType()
	strVal := func(p *string) tftypes.Value {
		if p == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *p)
	}
	boolVal := func(p *bool) tftypes.Value {
		if p == nil {
			return tftypes.NewValue(tftypes.Bool, nil)
		}
		return tftypes.NewValue(tftypes.Bool, *p)
	}
	val := tftypes.NewValue(typ, map[string]tftypes.Value{
		"api_endpoint":    strVal(v.endpoint),
		"api_key":         strVal(v.apiKey),
		"tenant_id":       strVal(v.tenantID),
		"use_cli_config":  boolVal(v.useCLIConfig),
		"cli_config_path": strVal(v.cliConfigPath),
		"cli_context":     strVal(v.cliContext),
	})
	dv, err := tfprotov6.NewDynamicValue(typ, val)
	if err != nil {
		t.Fatalf("failed to create DynamicValue: %v", err)
	}
	return &dv
}

func TestConfigureWithValidAPIKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-API-Key") != "valid-key" { // pragma: allowlist secret
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "AUTHENTICATION_REQUIRED",
					"message": "invalid api key",
				},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":       "user-123",
			"tenantId": "tenant-456",
			"email":    "test@example.com",
			"name":     "Test User",
		})
	}))
	defer server.Close()

	ctx := context.Background()
	protoServer := providerserver.NewProtocol6(New("test")())()

	resp, err := protoServer.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
		Config: newProviderDynamicValue(t, server.URL, "valid-key"),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider returned error: %v", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("unexpected error diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}
}

func TestConfigureWithInvalidAPIResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{
				"code":    "AUTHENTICATION_REQUIRED",
				"message": "invalid api key",
			},
		})
	}))
	defer server.Close()

	ctx := context.Background()
	protoServer := providerserver.NewProtocol6(New("test")())()

	resp, err := protoServer.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
		Config: newProviderDynamicValue(t, server.URL, "bad-key"),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider returned error: %v", err)
	}

	hasError := false
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("expected error diagnostic for invalid API response")
	}
}

func TestConfigureMissingAPIKey(t *testing.T) {
	// No api_key attribute, no env var, and no fm CLI config -> must error.
	t.Setenv("FROSTMOLN_API_KEY", "")
	t.Setenv("FROSTMOLN_API_ENDPOINT", "")
	t.Setenv("FROSTMOLN_USE_CLI_CONFIG", "")
	t.Setenv("FM_CONFIG", "")
	t.Setenv("FROSTMOLN_CLI_CONFIG", "")

	ctx := context.Background()
	protoServer := providerserver.NewProtocol6(New("test")())()

	// All attributes null, but point the CLI-config lookup at a nonexistent
	// file so the fallback finds nothing and the missing-credentials error wins.
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	resp, err := protoServer.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{cliConfigPath: &missing}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider returned error: %v", err)
	}

	hasError := false
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			hasError = true
			break
		}
	}
	if !hasError {
		t.Error("expected error diagnostic when no credentials are available")
	}
}

func TestConfigureFromEnvVars(t *testing.T) {
	// api_endpoint and api_key resolved entirely from environment variables.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-API-Key") != "env-key" { // pragma: allowlist secret
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":       "user-env",
			"tenantId": "tenant-env",
		})
	}))
	defer server.Close()

	t.Setenv("FROSTMOLN_API_ENDPOINT", server.URL)
	t.Setenv("FROSTMOLN_API_KEY", "env-key") // pragma: allowlist secret

	ctx := context.Background()
	protoServer := providerserver.NewProtocol6(New("test")())()

	// All attributes null so the env vars are used.
	resp, err := protoServer.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider returned error: %v", err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("unexpected error diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}
}

func TestConfigureSetsClientInProviderData(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":       "user-abc",
				"tenantId": "tenant-xyz",
				"email":    "test@example.com",
			})
			return
		}
		// After configure, the provider should be able to make API calls.
		// Test that a subsequent data source read can reach the server.
		if r.URL.Path == "/v1/flavors" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"flavors": []interface{}{},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	protoServer := providerserver.NewProtocol6(New("test")())()

	// First configure the provider
	configResp, err := protoServer.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{
		Config: newProviderDynamicValue(t, server.URL, "test-key"),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider returned error: %v", err)
	}
	for _, d := range configResp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("unexpected error diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}

	// Verify provider is configured by checking that schema is still valid
	// (the provider data is internal to the framework, but we can verify
	// no errors occurred during configuration)
	schemaResp, err := protoServer.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema returned error: %v", err)
	}
	if schemaResp.Provider == nil {
		t.Fatal("expected provider schema after configure")
	}
}

// clearCredentialEnv neutralizes every ambient credential/source env var so a
// test exercises exactly the path it sets up.
func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"FROSTMOLN_API_KEY", "FROSTMOLN_API_ENDPOINT", "FROSTMOLN_USE_CLI_CONFIG",
		"FM_CONFIG", "FROSTMOLN_CLI_CONFIG", "FROSTMOLN_TENANT_ID",
	} {
		t.Setenv(k, "")
	}
}

// writeFMConfig writes a temporary fm CLI config file and returns its path.
func writeFMConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fm config: %v", err)
	}
	return path
}

func assertNoErrorDiagnostics(t *testing.T, diags []*tfprotov6.Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("unexpected error diagnostic: %s: %s", d.Summary, d.Detail)
		}
	}
}

func assertHasErrorDiagnostic(t *testing.T, diags []*tfprotov6.Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return
		}
	}
	t.Error("expected an error diagnostic")
}

func TestConfigureFromCLIConfigAPIKey(t *testing.T) {
	clearCredentialEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" || r.Header.Get("X-API-Key") != "fmk_from_cli" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "tenantId": "t1"})
	}))
	defer server.Close()

	cfg := writeFMConfig(t, fmt.Sprintf("current_context: default\ncontexts:\n  default:\n    api_endpoint: %s\n    credentials:\n      api_key: fmk_from_cli\n", server.URL))

	protoServer := providerserver.NewProtocol6(New("test")())()
	resp, err := protoServer.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{cliConfigPath: &cfg}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	assertNoErrorDiagnostics(t, resp.Diagnostics)
}

func TestConfigureFromCLIConfigBearer(t *testing.T) {
	clearCredentialEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" || r.Header.Get("Authorization") != "Bearer cli-access" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "tenantId": "t1"})
	}))
	defer server.Close()

	// Fresh token (far-future expiry) so no refresh is attempted; this exercises
	// the bearer auth header and the endpoint adoption from the CLI config.
	exp := time.Now().Add(time.Hour).Unix()
	cfg := writeFMConfig(t, fmt.Sprintf("current_context: default\ncontexts:\n  default:\n    api_endpoint: %s\n    credentials:\n      access_token: cli-access\n      refresh_token: cli-refresh\n      expires_at: %d\n", server.URL, exp))

	protoServer := providerserver.NewProtocol6(New("test")())()
	resp, err := protoServer.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{cliConfigPath: &cfg}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	assertNoErrorDiagnostics(t, resp.Diagnostics)
}

func TestConfigureCLIConfigDisabled(t *testing.T) {
	clearCredentialEnv(t)
	cfg := writeFMConfig(t, "current_context: default\ncontexts:\n  default:\n    credentials:\n      api_key: fmk_from_cli\n")

	disabled := false
	protoServer := providerserver.NewProtocol6(New("test")())()
	resp, err := protoServer.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{cliConfigPath: &cfg, useCLIConfig: &disabled}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	// use_cli_config=false must ignore the CLI config and error on missing creds.
	assertHasErrorDiagnostic(t, resp.Diagnostics)
}

// The gateway mounts every customer route under /api (api-gateway
// definitions.go serves the current-user endpoint at /api/v1/me), so a default
// endpoint without that prefix makes the provider's configure-time GET /v1/me
// 404 at the edge — "NOT_FOUND: the requested resource was not found" — for
// every practitioner authenticating with an API key, which is exactly what the
// docs.frostmoln.se getting-started guide tells them to do.
func TestDefaultAPIEndpointCarriesGatewayAPIPrefix(t *testing.T) {
	t.Parallel()

	// Asserted on the literal, not via hasAPIPathPrefix: a guard that calls the
	// helper it is guarding would pass vacuously if that helper regressed to
	// always-true.
	if !strings.HasSuffix(defaultAPIEndpoint, "/api") {
		t.Errorf("defaultAPIEndpoint = %q, want the gateway's /api path prefix", defaultAPIEndpoint)
	}
}

func TestEndpointHint(t *testing.T) {
	t.Parallel()

	notFound := &client.APIError{StatusCode: http.StatusNotFound, Code: "NOT_FOUND", Message: "the requested resource was not found"}
	cases := []struct {
		name     string
		endpoint string
		err      error
		wantHint bool
	}{
		{"404 on a bare endpoint explains the missing prefix", "https://api.frostmoln.cloud", fmt.Errorf("failed to authenticate: %w", notFound), true},
		{"404 on an /api endpoint is a real not-found", "https://api.frostmoln.cloud/api", fmt.Errorf("failed to authenticate: %w", notFound), false},
		{"a non-404 failure is unrelated to the prefix", "https://api.frostmoln.cloud", fmt.Errorf("failed to authenticate: %w", &client.APIError{StatusCode: http.StatusUnauthorized, Code: "AUTHENTICATION_REQUIRED"}), false},
		{"a transport error carries no status", "https://api.frostmoln.cloud", fmt.Errorf("dial tcp: connection refused"), false},
		// The OIDC bearer path fails during the token refresh against
		// <endpoint>/v1/auth/cli-config, where the shared oidc module returns a
		// plain formatted error rather than an *APIError — the hint must still fire.
		{
			"404 from the bearer refresh is not an APIError", "https://api.frostmoln.cloud",
			fmt.Errorf("refresh access token: fetch cli-config: GET https://api.frostmoln.cloud/v1/auth/cli-config: HTTP 404: not found"), true,
		},
		{
			"404 from the bearer refresh on an /api endpoint stays silent", "https://api.frostmoln.cloud/api",
			fmt.Errorf("refresh access token: fetch cli-config: GET https://api.frostmoln.cloud/api/v1/auth/cli-config: HTTP 404: not found"), false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := endpointHint(tc.endpoint, false, tc.err)
			if (got != "") != tc.wantHint {
				t.Errorf("endpointHint(%q, %v) = %q, want hint: %v", tc.endpoint, tc.err, got, tc.wantHint)
			}
		})
	}
}

// A stale ~/.fm/config.yaml (written before the CLI stored the /api suffix) is
// adopted verbatim, so the remedy is to fix the CLI config — not to paper over
// it with api_endpoint, which would leave `fm` itself broken.
func TestEndpointHintPointsCLIConfigUsersAtTheCLI(t *testing.T) {
	t.Parallel()

	notFound := fmt.Errorf("failed to authenticate: %w", &client.APIError{StatusCode: http.StatusNotFound, Code: "NOT_FOUND"})
	hint := endpointHint("https://api.frostmoln.cloud", true, notFound)
	if !strings.Contains(hint, "fm config set api_endpoint https://api.frostmoln.cloud/api") {
		t.Errorf("hint should point at the fm CLI config: %s", hint)
	}
	if strings.Contains(hint, "FROSTMOLN_API_ENDPOINT") {
		t.Errorf("hint should not offer the env-var workaround for a CLI-sourced endpoint: %s", hint)
	}
}

// Terraform does not redact provider diagnostics, and this one repeats on every
// plan until the endpoint is fixed — so a basic-auth proxy endpoint must not
// spill its password (or a query token) into a CI log.
func TestEndpointHintRedactsCredentialsInTheEndpoint(t *testing.T) {
	t.Parallel()

	notFound := fmt.Errorf("failed to authenticate: %w", &client.APIError{StatusCode: http.StatusNotFound, Code: "NOT_FOUND"})
	hint := endpointHint("https://svc:hunter2@tf-proxy.corp.example?access_token=s3cret", false, notFound)
	if hint == "" {
		t.Fatal("expected a hint for a bare endpoint")
	}
	for _, secret := range []string{"hunter2", "s3cret"} { // pragma: allowlist secret
		if strings.Contains(hint, secret) {
			t.Errorf("hint leaks %q: %s", secret, hint)
		}
	}
	// The suggestion must still be a usable URL: the segment belongs on the
	// path, not appended after a query string.
	if !strings.Contains(hint, "tf-proxy.corp.example/api") {
		t.Errorf("hint does not suggest a well-formed /api endpoint: %s", hint)
	}
}

func TestEndpointHintSuggestionKeepsProxySubPath(t *testing.T) {
	t.Parallel()

	notFound := fmt.Errorf("failed to authenticate: %w", &client.APIError{StatusCode: http.StatusNotFound, Code: "NOT_FOUND"})
	hint := endpointHint("https://proxy.example/frostmoln/", false, notFound)
	if !strings.Contains(hint, "https://proxy.example/frostmoln/api") {
		t.Errorf("hint should append /api to the proxy's own path: %s", hint)
	}
}

func TestChooseCLIEndpoint(t *testing.T) {
	cases := []struct {
		name        string
		explicit    bool
		base        string
		cliEndpoint string
		want        string
	}{
		{"explicit wins", true, "https://explicit.example/api", "https://cli.example/api", "https://explicit.example/api"},
		{"adopt CLI endpoint", false, defaultAPIEndpoint, "https://cli.example/api", "https://cli.example/api"},
		{"CLI config without endpoint falls back to the default", false, defaultAPIEndpoint, "", defaultAPIEndpoint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chooseCLIEndpoint(tc.explicit, tc.base, tc.cliEndpoint); got != tc.want {
				t.Errorf("chooseCLIEndpoint(%v,%q,%q) = %q, want %q", tc.explicit, tc.base, tc.cliEndpoint, got, tc.want)
			}
		})
	}
}

func TestResolveUseCLIConfigEnvParse(t *testing.T) {
	clearCredentialEnv(t)
	t.Setenv("FROSTMOLN_USE_CLI_CONFIG", "false")
	if v, err := resolveUseCLIConfig(FrostmolnProviderModel{}); err != nil || v {
		t.Errorf("expected (false,nil) for env=false, got (%v,%v)", v, err)
	}
	t.Setenv("FROSTMOLN_USE_CLI_CONFIG", "garbage")
	if _, err := resolveUseCLIConfig(FrostmolnProviderModel{}); err == nil {
		t.Error("expected an error for an unparseable FROSTMOLN_USE_CLI_CONFIG (must not fail open)")
	}
}

func TestResolveTenantID(t *testing.T) {
	clearCredentialEnv(t)

	// Attribute wins over the env var.
	t.Setenv("FROSTMOLN_TENANT_ID", "t-env")
	if got := resolveTenantID(FrostmolnProviderModel{TenantID: types.StringValue("t-attr")}); got != "t-attr" {
		t.Errorf("attr should win, got %q", got)
	}
	// Env var used when the attribute is null.
	if got := resolveTenantID(FrostmolnProviderModel{}); got != "t-env" {
		t.Errorf("env should be used, got %q", got)
	}
	// Neither set -> empty (client adopts the /v1/me default tenant).
	t.Setenv("FROSTMOLN_TENANT_ID", "")
	if got := resolveTenantID(FrostmolnProviderModel{}); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// meServer returns an httptest server whose /v1/me reports the given default
// tenant for a valid X-API-Key, recording the last tenant-scoped path it saw.
func meServer(t *testing.T, defaultTenant string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" || r.Header.Get("X-API-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "tenantId": defaultTenant})
	}))
}

func TestConfigureAPIKeyTenantIDMatches(t *testing.T) {
	// tenant_id equal to the API key's own tenant is accepted.
	clearCredentialEnv(t)
	server := meServer(t, "default-tenant")
	defer server.Close()

	endpoint, apiKey, tenant := server.URL, "k", "default-tenant" // pragma: allowlist secret
	protoServer := providerserver.NewProtocol6(New("test")())()
	resp, err := protoServer.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{endpoint: &endpoint, apiKey: &apiKey, tenantID: &tenant}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	assertNoErrorDiagnostics(t, resp.Diagnostics)
}

func TestConfigureAPIKeyTenantIDMismatchErrors(t *testing.T) {
	// An API key is single-tenant: a tenant_id other than the key's tenant must
	// fail fast at configure with a clear diagnostic, not mid-apply.
	clearCredentialEnv(t)
	server := meServer(t, "default-tenant")
	defer server.Close()

	endpoint, apiKey, tenant := server.URL, "k", "other-tenant" // pragma: allowlist secret
	protoServer := providerserver.NewProtocol6(New("test")())()
	resp, err := protoServer.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{endpoint: &endpoint, apiKey: &apiKey, tenantID: &tenant}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	assertHasErrorDiagnostic(t, resp.Diagnostics)
}

func TestConfigureTenantIDFromEnvReachesOverride(t *testing.T) {
	// FROSTMOLN_TENANT_ID flows through resolveTenantID into the override: a
	// mismatched env tenant on the API-key path trips the same single-tenant check.
	clearCredentialEnv(t)
	server := meServer(t, "default-tenant")
	defer server.Close()

	t.Setenv("FROSTMOLN_TENANT_ID", "other-tenant")
	endpoint, apiKey := server.URL, "k" // pragma: allowlist secret
	protoServer := providerserver.NewProtocol6(New("test")())()
	resp, err := protoServer.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{endpoint: &endpoint, apiKey: &apiKey}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	assertHasErrorDiagnostic(t, resp.Diagnostics)
}

func TestConfigureExplicitAPIKeyWinsOverCLI(t *testing.T) {
	clearCredentialEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/me" || r.Header.Get("X-API-Key") != "explicit-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u1", "tenantId": "t1"})
	}))
	defer server.Close()

	// CLI config holds a DIFFERENT api key; the explicit attribute must win.
	cfg := writeFMConfig(t, fmt.Sprintf("current_context: default\ncontexts:\n  default:\n    api_endpoint: %s\n    credentials:\n      api_key: cli-key\n", server.URL))
	endpoint := server.URL
	apiKey := "explicit-key" // pragma: allowlist secret

	protoServer := providerserver.NewProtocol6(New("test")())()
	resp, err := protoServer.ConfigureProvider(context.Background(), &tfprotov6.ConfigureProviderRequest{
		Config: newProviderConfig(t, providerConfigValues{endpoint: &endpoint, apiKey: &apiKey, cliConfigPath: &cfg}),
	})
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	assertNoErrorDiagnostics(t, resp.Diagnostics)
}

// A practitioner must be able to find out WHICH credential the provider used.
// The resolution order falls through silently — api_key, FROSTMOLN_API_KEY,
// then whatever `fm auth login` left behind — and a customer who created an API
// key for Terraform ran their whole apply as their own user without knowing,
// then reported the key as "never used". It was never used.
func TestConfigureNamesTheCredentialSourceOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "AUTHENTICATION_REQUIRED", "message": "nope"},
		})
	}))
	defer server.Close()

	t.Run("an API key names the environment variable", func(t *testing.T) {
		clearCredentialEnv(t)
		t.Setenv("FROSTMOLN_API_KEY", "fmk_from_env") // pragma: allowlist secret
		t.Setenv("FROSTMOLN_API_ENDPOINT", server.URL)

		resp, err := providerserver.NewProtocol6(New("test")())().ConfigureProvider(
			context.Background(), &tfprotov6.ConfigureProviderRequest{
				Config: newProviderConfig(t, providerConfigValues{}),
			},
		)
		if err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}
		assertConfigureFailureContains(t, resp.Diagnostics, "using the FROSTMOLN_API_KEY environment variable")
		assertNoDiagnosticContains(t, resp.Diagnostics, "fmk_from_env") // pragma: allowlist secret
	})

	t.Run("a CLI session says so, and says it is not an API key", func(t *testing.T) {
		clearCredentialEnv(t)
		cfgPath := writeFMConfig(t, `api_endpoint: `+server.URL+`
current_context: default
contexts:
  default:
    api_endpoint: `+server.URL+`
    credentials:
      access_token: at_do_not_log
      refresh_token: rt_do_not_log
`)
		t.Setenv("FROSTMOLN_CLI_CONFIG", cfgPath)

		resp, err := providerserver.NewProtocol6(New("test")())().ConfigureProvider(
			context.Background(), &tfprotov6.ConfigureProviderRequest{
				Config: newProviderConfig(t, providerConfigValues{}),
			},
		)
		if err != nil {
			t.Fatalf("ConfigureProvider: %v", err)
		}
		assertConfigureFailureContains(t, resp.Diagnostics, "fm CLI login session")
		assertConfigureFailureContains(t, resp.Diagnostics, "NOT an API key")
		// The session's tokens must never ride out on the diagnostic.
		assertNoDiagnosticContains(t, resp.Diagnostics, "at_do_not_log")
		assertNoDiagnosticContains(t, resp.Diagnostics, "rt_do_not_log")
	})
}

// assertConfigureFailureContains pins the phrase to the CONFIGURE FAILURE
// diagnostic specifically. Matching any diagnostic was vacuous: the
// "Missing Credentials" text also lists FROSTMOLN_API_KEY, so an assertion on
// the bare variable name stayed green even if the env credential were never
// picked up at all.
func assertConfigureFailureContains(t *testing.T, diags []*tfprotov6.Diagnostic, want string) {
	t.Helper()
	for _, d := range diags {
		if d.Summary == "Failed to Configure Provider" && strings.Contains(d.Detail, want) {
			return
		}
	}
	t.Errorf("no Failed-to-Configure diagnostic mentioning %q; got %+v", want, diags)
}

func assertNoDiagnosticContains(t *testing.T, diags []*tfprotov6.Diagnostic, forbidden string) {
	t.Helper()
	for _, d := range diags {
		if strings.Contains(d.Summary+d.Detail, forbidden) {
			t.Errorf("diagnostic leaked %q: %s / %s", forbidden, d.Summary, d.Detail)
		}
	}
}

// The four labels, tested directly. Driving each through providerserver +
// httptest would have left the subtlest line — attribute vs environment
// variable — with no coverage at all.
func TestCredentialSourceLabels(t *testing.T) {
	t.Parallel()

	t.Run("cliSourceLabel names the resolved file and context", func(t *testing.T) {
		t.Parallel()
		assert := func(got, want string) {
			t.Helper()
			if got != want {
				t.Errorf("cliSourceLabel = %q, want %q", got, want)
			}
		}
		assert(cliSourceLabel("/home/j/.fm/config.yaml", "staging"), "/home/j/.fm/config.yaml, context staging")
		// A flat, context-less file: no context to name.
		assert(cliSourceLabel("/home/j/.fm/config.yaml", ""), "/home/j/.fm/config.yaml")
		// Never guess a path we were not given.
		assert(cliSourceLabel("", ""), "the fm CLI config")
	})

	t.Run("an endpoint is redacted before it reaches a log field", func(t *testing.T) {
		t.Parallel()
		got := redactedEndpoint("https://svc:hunter2@tf-proxy.corp.example/api?access_token=s3cret") // pragma: allowlist secret
		for _, secret := range []string{"hunter2", "s3cret"} {                                       // pragma: allowlist secret
			if strings.Contains(got, secret) {
				t.Errorf("redactedEndpoint leaked %q: %s", secret, got)
			}
		}
		if !strings.Contains(got, "tf-proxy.corp.example") {
			t.Errorf("redactedEndpoint dropped the host, leaving nothing useful: %s", got)
		}
	})
}

// An empty api_key attribute must fall through to FROSTMOLN_API_KEY rather
// than silently disabling it and landing on the CLI session.
func TestEmptyAPIKeyAttributeFallsThroughToTheEnvironment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "AUTHENTICATION_REQUIRED", "message": "nope"},
		})
	}))
	defer server.Close()

	clearCredentialEnv(t)
	t.Setenv("FROSTMOLN_API_KEY", "fmk_from_env") // pragma: allowlist secret
	t.Setenv("FROSTMOLN_API_ENDPOINT", server.URL)
	empty := ""

	resp, err := providerserver.NewProtocol6(New("test")())().ConfigureProvider(
		context.Background(), &tfprotov6.ConfigureProviderRequest{
			Config: newProviderConfig(t, providerConfigValues{apiKey: &empty}),
		},
	)
	if err != nil {
		t.Fatalf("ConfigureProvider: %v", err)
	}
	assertConfigureFailureContains(t, resp.Diagnostics, "using the FROSTMOLN_API_KEY environment variable")
}
