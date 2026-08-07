package egress_gateway_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/provider"
	egressgateway "go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/egress_gateway"
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

// egressGatewayConfig builds a resource config with every attribute null except
// vpc_id and mode, taken from the resource's own schema so the shape cannot
// drift away from it.
func egressGatewayConfig(t *testing.T, mode string) *tfprotov6.DynamicValue {
	t.Helper()
	ctx := context.Background()

	schemaResp := &fwresource.SchemaResponse{}
	egressgateway.NewResource().Schema(ctx, fwresource.SchemaRequest{}, schemaResp)
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
		TypeName: "frostmoln_egress_gateway",
		Config:   egressGatewayConfig(t, mode),
	})
	if err != nil {
		t.Fatalf("ValidateResourceConfig returned error: %v", err)
	}
	return resp.Diagnostics
}

func egressDiagText(diags []*tfprotov6.Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary)
		b.WriteString("\n")
		b.WriteString(d.Detail)
		b.WriteString("\n")
	}
	return b.String()
}

func egressDiagsHaveError(diags []*tfprotov6.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

// TestEgressGatewayValidateResourceConfigRefusesNAT is the falsifier for the
// withdrawal wording. `mode = "nat"` must fail the validate RPC itself — which
// is what makes "while it is still in the configuration, validate/plan/refresh/
// import all stop here" true, and what made the old "they all still read it"
// false.
func TestEgressGatewayValidateResourceConfigRefusesNAT(t *testing.T) {
	t.Parallel()

	diags := validateEgressGatewayConfig(t, "nat")
	if !egressDiagsHaveError(diags) {
		t.Fatal("mode = \"nat\" must be refused by ValidateResourceConfig; if it is not, " +
			"the schema's claim about when the refusal fires is wrong")
	}

	text := egressDiagText(diags)
	if !strings.Contains(strings.ToLower(text), "withdrawn") {
		t.Errorf("the refusal must name the withdrawal, got:\n%s", text)
	}
	// The refusal must not repeat the claim it replaced. "all still read it" told
	// the practitioner to leave `mode = "nat"` where it was.
	for _, forbidden := range []string{"all still read it", "Only setting the value is refused"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("the refusal still claims %q, which this very RPC contradicts:\n%s", forbidden, text)
		}
	}
}

// TestEgressGatewayValidateResourceConfigAcceptsPublicIP is the other half: the
// refusal must be specific to the withdrawn value, not a resource that fails
// validation for everyone.
func TestEgressGatewayValidateResourceConfigAcceptsPublicIP(t *testing.T) {
	t.Parallel()

	diags := validateEgressGatewayConfig(t, "public_ip")
	if egressDiagsHaveError(diags) {
		t.Errorf("mode = \"public_ip\" must validate cleanly, got:\n%s", egressDiagText(diags))
	}
}
