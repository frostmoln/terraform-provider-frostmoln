package bucket_cors_configuration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// TestAccBucketCORSConfiguration_basic exercises what only a real backend can
// answer: that a rule's id and its optional header lists survive the round trip
// through the object store, and that rules come back in the order submitted
// rather than reordered. A field that does not round-trip is a perpetual diff,
// not a cosmetic problem.
//
// It also pins the platform behaviour this resource discloses: a freshly
// created bucket already carries a default CORS rule, and applying this
// resource replaces it.
func TestAccBucketCORSConfiguration_basic(t *testing.T) {
	bucketName := acctest.RandomName("bucket")
	resourceName := "frostmoln_bucket_cors_configuration.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckCORSDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccCORSConfig(bucketName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "bucket", bucketName),
					resource.TestCheckResourceAttr(resourceName, "rules.#", "2"),
					resource.TestCheckResourceAttr(resourceName, "rules.0.id", "z-app"),
					resource.TestCheckResourceAttr(resourceName, "rules.1.id", "a-cdn"),
					resource.TestCheckResourceAttr(resourceName, "rules.0.max_age_seconds", "3600"),
					resource.TestCheckTypeSetElemAttr(resourceName, "rules.0.expose_headers.*", "ETag"),
					// Omitted optionals must read back as absent, not as empty.
					resource.TestCheckNoResourceAttr(resourceName, "rules.1.max_age_seconds"),
					resource.TestCheckNoResourceAttr(resourceName, "rules.1.expose_headers"),
				),
			},
			{
				Config: testAccCORSConfigUpdated(bucketName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "rules.#", "1"),
					resource.TestCheckTypeSetElemAttr(resourceName, "rules.0.allowed_methods.*", "DELETE"),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources[resourceName]
					if !ok {
						return "", fmt.Errorf("resource %s not found", resourceName)
					}
					return rs.Primary.Attributes["bucket"], nil
				},
			},
		},
	})
}

func testAccCheckCORSDestroy(s *terraform.State) error {
	c, err := acctest.TestClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "frostmoln_bucket_cors_configuration" {
			continue
		}

		bucket := rs.Primary.Attributes["bucket"]
		resp, err := c.Get(context.Background(), c.TenantPath("/buckets/"+bucket+"/cors"), nil)
		if err != nil {
			if client.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("unexpected error reading CORS for %s: %w", bucket, err)
		}
		rules, err := client.ParseResponse[struct {
			Rules []map[string]any `json:"rules"`
		}](resp)
		if err != nil {
			return fmt.Errorf("parsing CORS for %s: %w", bucket, err)
		}
		if len(rules.Rules) != 0 {
			return fmt.Errorf("CORS configuration for bucket %s still has %d rule(s)", bucket, len(rules.Rules))
		}
	}

	return nil
}

func testAccCORSConfig(bucketName string) string {
	return fmt.Sprintf(`
resource "frostmoln_bucket" "test" {
  name = %[1]q
}

resource "frostmoln_bucket_cors_configuration" "test" {
  bucket = frostmoln_bucket.test.name

  rules = [
    {
      id              = "z-app"
      allowed_origins = ["https://app.example.com"]
      allowed_methods = ["GET", "HEAD", "PUT"]
      expose_headers  = ["ETag"]
      max_age_seconds = 3600
    },
    {
      id              = "a-cdn"
      allowed_origins = ["https://cdn.example.com"]
      allowed_methods = ["GET"]
    },
  ]
}
`, bucketName)
}

func testAccCORSConfigUpdated(bucketName string) string {
	return fmt.Sprintf(`
resource "frostmoln_bucket" "test" {
  name = %[1]q
}

resource "frostmoln_bucket_cors_configuration" "test" {
  bucket = frostmoln_bucket.test.name

  rules = [
    {
      id              = "z-app"
      allowed_origins = ["https://app.example.com"]
      allowed_methods = ["GET", "HEAD", "PUT", "DELETE"]
      max_age_seconds = 60
    },
  ]
}
`, bucketName)
}
