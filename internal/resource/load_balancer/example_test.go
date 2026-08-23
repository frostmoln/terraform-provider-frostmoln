package load_balancer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exampleHCL = "../../../examples/resources/frostmoln_load_balancer/resource.tf"

func readExample(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(exampleHCL))
	if err != nil {
		t.Fatalf("cannot read the shipped example: %v", err)
	}
	return string(raw)
}

// TestExampleCarriesNoInternalNames: the example renders onto the public
// Terraform Registry and into docs/resources/load_balancer.md. Internal
// component names publish platform topology and mean nothing to a customer.
//
// The sibling resources (gateway, public_ip, public_ip_association) have
// carried this guard for a while; load_balancer did not, which is precisely why
// its example still described "the amphora and ovn providers" and shipped the
// Octavia driver names to the Registry long after the rest of the surface had
// been cleaned. The guard belongs on every example, not on the ones that
// happened to get one.
func TestExampleCarriesNoInternalNames(t *testing.T) {
	example := strings.ToLower(readExample(t))
	for _, term := range []string{"amphora", "octavia", "neutron", "ovn", "openstack", "shard", "managed-agent"} {
		if strings.Contains(example, term) {
			t.Errorf("the shipped example names the internal component %q", term)
		}
	}
}

// TestExampleUsesTheCurrentTypeAttribute guards against the example drifting
// back to the pre-rename attribute, which would no longer even be valid config.
func TestExampleUsesTheCurrentTypeAttribute(t *testing.T) {
	example := readExample(t)
	if strings.Contains(example, "provider_type") {
		t.Error("the example uses provider_type, which the current schema does not accept")
	}
	if !strings.Contains(example, `type      = "l7"`) && !strings.Contains(example, `type = "l7"`) {
		t.Error("the example should demonstrate the type attribute")
	}
}
