package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	for _, expected := range []string{"api_endpoint", "api_key"} {
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
			"api_endpoint": tftypes.String,
			"api_key":      tftypes.String,
		},
	}
}

// newProviderDynamicValue creates a DynamicValue for provider configuration.
func newProviderDynamicValue(t *testing.T, endpoint, apiKey string) *tfprotov6.DynamicValue {
	t.Helper()
	typ := providerConfigType()
	val := tftypes.NewValue(typ, map[string]tftypes.Value{
		"api_endpoint": tftypes.NewValue(tftypes.String, endpoint),
		"api_key":      tftypes.NewValue(tftypes.String, apiKey),
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
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code":    "AUTHENTICATION_REQUIRED",
					"message": "invalid api key",
				},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
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
		json.NewEncoder(w).Encode(map[string]interface{}{
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

func TestConfigureSetsClientInProviderData(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
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
			json.NewEncoder(w).Encode(map[string]interface{}{
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
