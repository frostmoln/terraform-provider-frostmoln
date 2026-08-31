package image_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// testAccPreCheckCustomImages gates the BYOI acceptance test. It uploads a real
// disk image and spends one of the tenant's few hourly upload mints, so it
// requires all three of:
//
//  1. An explicit opt-in: FROSTMOLN_TEST_CUSTOM_IMAGES=1. This is NOT a feature
//     gate — BYOI opened to every tenant on 2026-08-22 and the `custom-images`
//     entitlement (ADR-0038) was removed. The opt-in survives because the test
//     spends real resources: it uploads a disk image and consumes one of the
//     tenant's few hourly upload mints, which is not something a nightly run
//     should do by default.
//  2. FROSTMOLN_TEST_IMAGE_FILE pointing at a local qcow2 to upload. There is no
//     sensible default: a synthetic file is not a bootable disk image and Glance
//     would refuse to convert it.
//  3. A tenant holding the entitlement. The probe hits the image list — the
//     surface the test uses — and skips loudly on 403.
//
// It is called only from TestCase.PreCheck, which the framework runs only under
// TF_ACC — a unit-test run never reaches it.
func testAccPreCheckCustomImages(t *testing.T) {
	t.Helper()
	acctest.TestAccPreCheck(t)

	if os.Getenv("FROSTMOLN_TEST_CUSTOM_IMAGES") != "1" {
		t.Skip("SKIPPING custom-image acceptance test: set FROSTMOLN_TEST_CUSTOM_IMAGES=1 to opt in " +
			"(this test uploads a real disk image and spends an upload mint)")
	}
	sourceFile := os.Getenv("FROSTMOLN_TEST_IMAGE_FILE")
	if sourceFile == "" {
		t.Skip("SKIPPING custom-image acceptance test: FROSTMOLN_TEST_IMAGE_FILE must point at a local qcow2 to upload")
	}
	if _, err := os.Stat(sourceFile); err != nil {
		t.Skipf("SKIPPING custom-image acceptance test: FROSTMOLN_TEST_IMAGE_FILE %q is not readable: %s", sourceFile, err)
	}

	c, err := acctest.TestClient()
	if err != nil {
		t.Fatalf("failed to build test client for the custom-images entitlement probe: %s", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.Get(ctx, c.TenantPath("/images"), nil); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
			t.Skipf("SKIPPING custom-image acceptance test: 403 from the image API. BYOI is no longer "+
				"entitlement-gated, so this is a scope/IAM refusal or a staging-quota cap rather than a "+
				"missing product: %s", err)
		}
		t.Fatalf("image-list probe failed with an unexpected error: %s", err)
	}
}

func TestAccImage_basic(t *testing.T) {
	// Read outside the PreCheck so the config strings can be built; the PreCheck
	// is what actually decides whether the test runs at all.
	sourceFile := os.Getenv("FROSTMOLN_TEST_IMAGE_FILE")
	name := acctest.RandomName("img")
	resourceName := "frostmoln_image.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheckCustomImages(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckImageDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccImageConfig(name, sourceFile, "first"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttr(resourceName, "status", "active"),
					resource.TestCheckResourceAttr(resourceName, "visibility", "private"),
					resource.TestCheckResourceAttrSet(resourceName, "checksum"),
				),
			},
			{
				// Metadata-only change: must update in place, never re-upload.
				Config: testAccImageConfig(name, sourceFile, "renamed"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "description", "renamed"),
					resource.TestCheckResourceAttr(resourceName, "status", "active"),
				),
			},
		},
	})
}

func testAccImageConfig(name, sourceFile, description string) string {
	return fmt.Sprintf(`
resource "frostmoln_image" "test" {
  name        = %q
  description = %q
  source_file = %q
  disk_format = "qcow2"
}
`, name, description, sourceFile)
}

// testAccCheckImageDestroy verifies the image is gone. Images ARE tenant-scoped
// — compute serves the full /v1/tenants/{id}/images surface, and only that path
// makes the gateway re-scope the signed auth context onto the tenant the
// provider is configured for — so this is the shared helper.
var testAccCheckImageDestroy = acctest.CheckDestroyByTenantPath("frostmoln_image", "/images")
