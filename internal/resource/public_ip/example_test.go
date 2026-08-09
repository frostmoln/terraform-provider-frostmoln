package public_ip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleHCL is the shipped example for this resource. tfplugindocs renders it
// verbatim into docs/resources/public_ip.md and from there onto the Terraform
// Registry, so it is the snippet practitioners copy.
const exampleHCL = "../../../examples/resources/frostmoln_public_ip/resource.tf"

func readExample(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(exampleHCL))
	if err != nil {
		t.Fatalf("cannot read the shipped example: %v", err)
	}
	return string(raw)
}

// TestExampleShowsPreventDestroy.
//
// This is the one resource on the platform whose destruction cannot be undone:
// the address returns to a shared regional pool and is re-issued to whoever
// asks next, so re-applying gets a different one. `prevent_destroy` is the only
// guard that fails the PLAN, which is what stops the cases nothing else can —
// a module removal, a `destroy -target`, CI running `destroy -auto-approve`.
//
// A practitioner who has never met it will not invent it, so the shipped
// example has to teach it.
func TestExampleShowsPreventDestroy(t *testing.T) {
	example := readExample(t)

	if !strings.Contains(example, "prevent_destroy = true") {
		t.Error("the example must show `lifecycle { prevent_destroy = true }` on an address others " +
			"depend on: releasing a public IP is irreversible and re-applying allocates a different one")
	}
	if !strings.Contains(strings.ToLower(example), "does not come back") &&
		!strings.Contains(strings.ToLower(example), "irreversible") {
		t.Error("the example must say WHY, in the practitioner's terms: the address does not come back")
	}
	if !strings.Contains(example, "remove the lifecycle block") &&
		!strings.Contains(example, "Remove the lifecycle block") {
		t.Error("the example must say how to retire an address deliberately, or the guard reads as a " +
			"dead end and gets copied without being understood")
	}
}

// TestExampleDoesNotPreArmTheAddressLossGate is a security regression guard, and
// the same one the gateway's example carries.
//
// `acknowledge_address_loss` is ORDINARY CONFIGURATION, so a `true` in the
// example is sticky: everyone who copies it carries a permanently disarmed gate
// for the life of the resource, and the next `terraform destroy` for any reason
// gives the address away with nothing in the plan beyond an ordinary "will be
// destroyed" line.
func TestExampleDoesNotPreArmTheAddressLossGate(t *testing.T) {
	example := readExample(t)

	for i, line := range strings.Split(example, "\n") {
		code, _, _ := strings.Cut(line, "#")
		if !strings.Contains(code, "acknowledge_address_loss") {
			continue
		}
		t.Errorf("example line %d arms the address-loss gate in live configuration, where it is sticky "+
			"for the life of the resource: %s", i+1, strings.TrimSpace(line))
	}

	if !strings.Contains(example, "acknowledge_address_loss") {
		t.Error("the example must still TEACH the acknowledgement (as a comment): a practitioner who " +
			"has never seen it meets it for the first time as a failed destroy")
	}
}

// TestExampleCarriesNoInternalNames: the example renders onto the public
// Terraform Registry. Internal component names publish platform topology and
// mean nothing to a customer.
func TestExampleCarriesNoInternalNames(t *testing.T) {
	example := strings.ToLower(readExample(t))
	for _, term := range []string{"shard", "managed-agent", "neutron", "ovn", "openstack", "floating ip"} {
		if strings.Contains(example, term) {
			t.Errorf("the shipped example names the internal component %q", term)
		}
	}
}
