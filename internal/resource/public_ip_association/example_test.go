package public_ip_association

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleHCL is the shipped example for this resource. tfplugindocs renders it
// verbatim into docs/resources/public_ip_association.md and from there onto the
// Terraform Registry, so it is the snippet practitioners copy.
const exampleHCL = "../../../examples/resources/frostmoln_public_ip_association/resource.tf"

func readExample(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(exampleHCL))
	if err != nil {
		t.Fatalf("cannot read the shipped example: %v", err)
	}
	return string(raw)
}

// TestExampleTeachesTheMutualExclusivity.
//
// `frostmoln_public_ip.instance_id` and this resource express the SAME
// attachment, and nothing in the framework can refuse a configuration that uses
// both — they are separate resources, and no cross-resource validation runs. A
// practitioner who uses both gets a configuration that never converges: each
// apply undoes the other's work, for ever.
//
// The schema descriptions say so, and so must the example, because the example
// is what gets copied. This is the guard the `frostmoln_public_ip` package
// carries for its own example.
func TestExampleTeachesTheMutualExclusivity(t *testing.T) {
	example := readExample(t)

	for _, want := range []string{"DO NOT DO THIS", "frostmoln_public_ip", "frostmoln_public_ip_association"} {
		if !strings.Contains(example, want) {
			t.Errorf("the example must show the conflict a reader cannot be protected from any other "+
				"way; %q is missing", want)
		}
	}
	if !strings.Contains(strings.ToLower(example), "mutually exclusive") {
		t.Error("the example must name the conflict as such: a reader who does not recognise it will " +
			"reach for both resources and never converge")
	}
}

// TestExampleDoesNotShipTheConflictAsLiveHCL is the regression guard that
// matters more than the prose.
//
// The wrong configuration is DEMONSTRATED in this example. If it is ever
// un-commented — or a `frostmoln_public_ip` in the example grows an
// `instance_id` next to the association that already manages that address — the
// snippet practitioners copy IS the non-converging configuration the surrounding
// paragraph warns them about.
func TestExampleDoesNotShipTheConflictAsLiveHCL(t *testing.T) {
	depth := 0
	inPublicIP := false

	for i, line := range strings.Split(readExample(t), "\n") {
		code, _, _ := strings.Cut(line, "#")

		if depth == 0 && strings.Contains(code, `resource "frostmoln_public_ip"`) {
			inPublicIP = true
		}
		if inPublicIP && strings.Contains(code, "instance_id") {
			t.Errorf("example line %d sets instance_id on a live frostmoln_public_ip resource: that "+
				"attribute and frostmoln_public_ip_association manage the same attachment, and the "+
				"example ships the very configuration it warns against: %s", i+1, strings.TrimSpace(line))
		}

		depth += strings.Count(code, "{") - strings.Count(code, "}")
		if depth <= 0 {
			depth = 0
			inPublicIP = false
		}
	}
}

// TestExampleSaysCreateBeforeDestroyDoesNotWork: every configurable attribute
// forces replacement, and `create_before_destroy` makes Terraform create the
// replacement while the old association still holds the address — which the
// platform refuses with 409. Reaching for it is the obvious way to try to close
// the gap a replacement leaves, so the example has to say it is not available.
func TestExampleSaysCreateBeforeDestroyDoesNotWork(t *testing.T) {
	if !strings.Contains(readExample(t), "create_before_destroy") {
		t.Error("the example must warn that create_before_destroy cannot be used on this resource: " +
			"the new association would be created while the old one still holds the address, and the " +
			"attach is refused with 409")
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

// TestExampleShowsTheGatewayOrdering pins the `depends_on` this example must
// teach, and the resource it belongs on.
//
// Terraform cannot derive the dependency — nothing in this resource refers to
// `frostmoln_gateway` — so an unaided practitioner meets it as a destroy that
// fails with GATEWAY_IN_USE half way through a teardown, or a create that fails
// with GATEWAY_EXISTS. The dependency belongs on the ATTACHMENT: pointing it the
// other way about (on the gateway, listing the addresses) reverses both orders
// and fixes neither.
func TestExampleShowsTheGatewayOrdering(t *testing.T) {
	example := readExample(t)

	if !strings.Contains(example, "depends_on = [frostmoln_gateway") {
		t.Error("the example must show depends_on on the association, pointing at the gateway: an " +
			"attached address cannot exist without a gateway and Terraform cannot see that")
	}
	// Matched with the trailing dot: "depends_on = [frostmoln_public_ip" also
	// rejects a legitimate depends_on naming an ASSOCIATION.
	if strings.Contains(example, "depends_on = [frostmoln_public_ip.") {
		t.Error("a depends_on pointing at the public IPs reverses both the create and the destroy " +
			"order, and leaves the destroy failing with GATEWAY_IN_USE")
	}
	for _, want := range []string{"GATEWAY_IN_USE", "GATEWAY_EXISTS"} {
		if !strings.Contains(example, want) {
			t.Errorf("the example must name %s — the failure this ordering prevents is how a reader "+
				"recognises the problem they already have", want)
		}
	}
}
