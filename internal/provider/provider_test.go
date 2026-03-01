package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
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
		"fm_ssh_key", "fm_bucket", "fm_s3_credential",
		"fm_vpc", "fm_subnet", "fm_security_group", "fm_security_group_rule",
		"fm_floating_ip", "fm_volume", "fm_volume_attachment", "fm_snapshot",
		"fm_instance", "fm_api_key",
	}
	for _, name := range expectedResources {
		if _, ok := resp.ResourceSchemas[name]; !ok {
			t.Errorf("expected resource schema %q", name)
		}
	}

	// Verify expected data sources exist
	expectedDataSources := []string{
		"fm_image", "fm_images", "fm_flavor", "fm_flavors",
		"fm_vpc", "fm_subnet", "fm_instance",
	}
	for _, name := range expectedDataSources {
		if _, ok := resp.DataSourceSchemas[name]; !ok {
			t.Errorf("expected data source schema %q", name)
		}
	}
}
