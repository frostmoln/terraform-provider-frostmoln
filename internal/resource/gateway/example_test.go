package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleHCL is the shipped example for this resource. It is not decoration:
// tfplugindocs renders it verbatim into docs/resources/gateway.md and
// from there onto the Terraform Registry, so it is the snippet practitioners
// copy into their own configurations.
const exampleHCL = "../../../examples/resources/frostmoln_gateway/resource.tf"

func readExample(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(exampleHCL))
	if err != nil {
		t.Fatalf("cannot read the shipped example: %v", err)
	}
	return string(raw)
}

// TestExampleDoesNotPreArmTheDestroyGate is a security regression guard.
//
// `acknowledge_connectivity_loss` is ORDINARY CONFIGURATION, so a `true` in the
// example is sticky: everyone who copies it carries a permanently disarmed
// destroy gate for the life of the resource. A later `terraform destroy
// -target`, a module removal, or a `vpc_id` change that forces replacement then
// takes the VPC's internet, DNS and managed-service connectivity down with
// nothing in the plan beyond an ordinary "will be destroyed" line — and, with
// out-of-band drift, Update would PATCH the mode back on Terraform's own
// initiative, re-addressing the VPC's outbound path and dropping in-flight connections.
//
// The attribute must therefore appear in the example only as commented
// guidance: set it, apply, do the thing, remove it again.
func TestExampleDoesNotPreArmTheDestroyGate(t *testing.T) {
	example := readExample(t)

	for i, line := range strings.Split(example, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if !strings.Contains(code, "acknowledge_connectivity_loss") {
			continue
		}
		t.Errorf("example line %d sets the destroy gate in live configuration, where it is sticky for the "+
			"life of the resource: %s", i+1, strings.TrimSpace(line))
	}

	if !strings.Contains(example, "acknowledge_connectivity_loss") {
		t.Error("the example must still TEACH the acknowledgement (as a comment): a practitioner who has " +
			"never seen it meets it for the first time as a failed destroy")
	}
	if !strings.Contains(strings.ToLower(example), "remove") {
		t.Error("the example must say the acknowledgement is removed from the configuration again once the " +
			"destroy or mode change is done")
	}
}

// TestExampleDoesNotOfferTheWithdrawnNATMode: the example is the snippet
// practitioners copy, so a live `mode = "nat"` in it hands them a
// configuration that fails validation on their first plan — and, worse, one
// that reads like the recommended shape. The withdrawal must still be TAUGHT
// (a reader arriving with a nat gateway needs to know it is withdrawn, not
// broken), so it may appear only in a comment.
func TestExampleDoesNotOfferTheWithdrawnNATMode(t *testing.T) {
	example := readExample(t)

	for i, line := range strings.Split(example, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if strings.Contains(code, `"nat"`) {
			t.Errorf("example line %d offers the withdrawn mode in live configuration: %s",
				i+1, strings.TrimSpace(line))
		}
	}

	if !strings.Contains(example, `"nat"`) {
		t.Error("the example must still explain (as a comment) that \"nat\" is withdrawn: a reader with " +
			"an existing NAT gateway otherwise learns it from a failed plan")
	}
	if !strings.Contains(strings.ToLower(example), "withdrawn") {
		t.Error("the example must name the withdrawal explicitly")
	}
	if !strings.Contains(example, `mode   = "public_ip"`) {
		t.Error("the example must show the mode that IS offered")
	}
}

// TestExampleCarriesNoInternalNames: the example renders onto the public
// Terraform Registry. Internal component names publish platform topology and
// mean nothing to a customer.
func TestExampleCarriesNoInternalNames(t *testing.T) {
	example := strings.ToLower(readExample(t))
	for _, term := range []string{"shard", "managed-agent", "neutron", "ovn", "openstack"} {
		if strings.Contains(example, term) {
			t.Errorf("the shipped example names the internal component %q", term)
		}
	}
}

// TestExampleOrdersPublicIPsAfterTheGateway pins the dependency direction the
// GATEWAY_IN_USE diagnostic also teaches: the `depends_on` belongs on
// the public IP, pointing at the gateway. Terraform creates the gateway first
// (an association into a gateway-less VPC makes the platform attach an implicit
// one, which then collides with an explicit `nat` gateway) and destroys the
// public IPs first (the gateway cannot be removed while they depend on it).
func TestExampleOrdersPublicIPsAfterTheGateway(t *testing.T) {
	example := readExample(t)
	if !strings.Contains(example, "depends_on = [frostmoln_gateway") {
		t.Error("the example must show depends_on on the public IP, pointing at the gateway")
	}
	if strings.Contains(example, "depends_on = [frostmoln_public_ip") {
		t.Error("a depends_on from the gateway to the public IPs reverses both the create and the destroy " +
			"order, and leaves the destroy failing with GATEWAY_IN_USE")
	}
}
