package provider

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// examplesDir is the shipped example tree, relative to this package.
const examplesDir = "../../examples"

// internalNamePatterns are the component names that must never reach a
// customer. Every example under examples/ renders onto the public Terraform
// Registry and into docs/, so a name here is published, not merely internal.
//
// Two groups, because a blanket case-insensitive match is wrong:
//
//   - unambiguous: no ordinary English meaning, so match case-insensitively.
//   - ambiguousEnglish: also ordinary words ("at a glance", "designate a
//     subnet", "ironic"). Matched CASE-SENSITIVELY, so the product name is
//     caught and the English word is not. A case-insensitive "glance" here
//     would fail on prose that is perfectly fine to ship.
//
// All patterns are word-bounded: an unanchored "ovn" would fire inside an
// unrelated identifier, and a guard that cries wolf gets deleted.
var internalNamePatterns = func() []*regexp.Regexp {
	unambiguous := []string{
		"openstack", "novalocal", "amphora", "octavia", "neutron", "ovn",
		"ceph", "rbd", "rgw", "kolla", "libvirt", "qemu", "rke2", "minio",
		"keycloak", "zitadel", "barbican", "gophercloud",
	}
	ambiguousEnglish := []string{
		"Nova", "Glance", "Cinder", "Designate", "Ironic", "Magnum",
		"Keystone", "Placement", "Temporal", "Swift",
	}
	out := make([]*regexp.Regexp, 0, len(unambiguous)+len(ambiguousEnglish))
	for _, t := range unambiguous {
		out = append(out, regexp.MustCompile(`(?i)\b`+t+`\b`))
	}
	for _, t := range ambiguousEnglish {
		out = append(out, regexp.MustCompile(`\b`+t+`\b`))
	}
	return out
}()

// TestNoExampleCarriesAnInternalName walks the WHOLE example tree.
//
// Four resources (load_balancer, gateway, public_ip, public_ip_association)
// already carry a per-resource version of this guard, and the load_balancer one
// says why it was added: its example shipped the Octavia driver names to the
// Registry "long after the rest of the surface had been cleaned", because it
// happened not to have a guard. Its own comment draws the conclusion — "the
// guard belongs on every example, not on the ones that happened to get one" —
// but the fix at the time was another per-resource copy.
//
// That left the same hole open: frostmoln_image's example described images as
// stored "on Ceph RBD", with no guard on that resource to catch it. A fifth
// copy would have left forty-odd more examples uncovered, and none of the four
// existing term lists contained "ceph" anyway, so even a copy would have
// passed. Walking the tree is what actually closes the class.
//
// The per-resource tests are deliberately kept: they also assert
// resource-specific things this one cannot, and their comments carry the
// history above.
func TestNoExampleCarriesAnInternalName(t *testing.T) {
	var checked int
	err := filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".tf") {
			return nil
		}
		raw, readErr := os.ReadFile(filepath.Clean(path))
		if readErr != nil {
			return readErr
		}
		checked++
		for _, re := range internalNamePatterns {
			if m := re.FindString(string(raw)); m != "" {
				t.Errorf("%s publishes the internal component name %q to the Terraform Registry", path, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot walk the example tree: %v", err)
	}
	// A typo in examplesDir would walk nothing and pass silently, which is the
	// failure mode that lets a guard look green while checking zero files.
	if checked == 0 {
		t.Fatalf("walked %s and found no .tf examples - the guard checked nothing", examplesDir)
	}
	t.Logf("checked %d example files", checked)
}
