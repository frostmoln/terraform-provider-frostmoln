package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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
	for _, expected := range []string{"api_endpoint", "api_key", "use_cli_config", "cli_config_path", "cli_context"} {
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
		"frostmoln_vpc", "frostmoln_subnet", "frostmoln_security_group", "frostmoln_security_group_rule",
		"frostmoln_floating_ip", "frostmoln_volume", "frostmoln_volume_attachment", "frostmoln_snapshot",
		"frostmoln_instance", "frostmoln_api_key",
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
		"FM_CONFIG", "FROSTMOLN_CLI_CONFIG",
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
		{"CLI config without endpoint falls back to /api default", false, defaultAPIEndpoint, "", defaultCLIAPIEndpoint},
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
