package gateway_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/provider"
	gatewaypkg "go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/gateway"
)

// ValidateResourceConfig is the RPC Terraform issues before `validate`, `plan`,
// `refresh` and `import` alike, and it is where an ATTRIBUTE VALIDATOR runs. The
// in-package tests call modeValidator directly, which proves the wording but
// says nothing about WHEN the refusal fires — and the wording is a claim about
// exactly that.
//
// The claim the schema and the diagnostic now make is: the gateway is
// unaffected, but while `mode = "nat"` is still written in the configuration
// none of those commands get past validation. That is only worth asserting
// against the real RPC: the previous text said `plan`, `refresh` and `import`
// "all still read it", which reads as "leave the configuration alone and
// everything but apply keeps working", and no direct-validator test could
// contradict it.

// gatewayConfig builds a resource config with every attribute null except
// vpc_id and mode, taken from the resource's own schema so the shape cannot
// drift away from it.
func gatewayConfig(t *testing.T, mode string) *tfprotov6.DynamicValue {
	t.Helper()
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	gatewaypkg.NewResource().Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", schemaResp.Diagnostics)
	}

	objType, ok := schemaResp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	if !ok {
		t.Fatalf("expected an object type, got %T", schemaResp.Schema.Type().TerraformType(ctx))
	}

	values := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, attrType := range objType.AttributeTypes {
		values[name] = tftypes.NewValue(attrType, nil)
	}
	values["vpc_id"] = tftypes.NewValue(tftypes.String, "vpc-1")
	values["mode"] = tftypes.NewValue(tftypes.String, mode)

	dv, err := tfprotov6.NewDynamicValue(objType, tftypes.NewValue(objType, values))
	if err != nil {
		t.Fatalf("failed to build config value: %v", err)
	}
	return &dv
}

func validateEgressGatewayConfig(t *testing.T, mode string) []*tfprotov6.Diagnostic {
	t.Helper()
	server := providerserver.NewProtocol6(provider.New("test")())()
	resp, err := server.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: "frostmoln_gateway",
		Config:   gatewayConfig(t, mode),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig returned error: %v", err)
	}
	return resp.Diagnostics
}

func gatewayDiagText(diags []*tfprotov6.Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary)
		b.WriteString("\n")
		b.WriteString(d.Detail)
		b.WriteString("\n")
	}
	return b.String()
}

func gatewayDiagsHaveError(diags []*tfprotov6.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

// TestGatewayValidateResourceConfigRefusesAnUnknownMode drives the real
// ValidateResourceConfig RPC, not the validator in isolation. That distinction
// is the point: it is what makes "validate, plan, refresh and import all stop
// here" true rather than assumed, and a unit test on the validator alone cannot
// establish it.
func TestGatewayValidateResourceConfigRefusesAnUnknownMode(t *testing.T) {
	t.Parallel()

	diags := validateEgressGatewayConfig(t, "not-a-mode")
	if !gatewayDiagsHaveError(diags) {
		t.Fatal("an unknown mode must be refused by ValidateResourceConfig; if it is not, " +
			"the schema's claim about when the refusal fires is wrong")
	}

	// The message has to name the one valid value. A refusal that only says
	// "invalid" leaves the practitioner guessing.
	if text := gatewayDiagText(diags); !strings.Contains(text, "public_ip") {
		t.Errorf("the refusal must name the accepted mode, got:\n%s", text)
	}
}

// TestEgressGatewayValidateResourceConfigAcceptsPublicIP is the other half: the
// refusal must be specific to the bad value, not a resource that fails
// validation for everyone.
func TestEgressGatewayValidateResourceConfigAcceptsPublicIP(t *testing.T) {
	t.Parallel()

	diags := validateEgressGatewayConfig(t, "public_ip")
	if gatewayDiagsHaveError(diags) {
		t.Errorf("mode = \"public_ip\" must validate cleanly, got:\n%s", gatewayDiagText(diags))
	}
}
