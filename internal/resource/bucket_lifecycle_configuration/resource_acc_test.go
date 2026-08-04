package bucket_lifecycle_configuration_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// TestAccBucketLifecycleConfiguration_basic exercises the two things only a
// real backend can answer, and which the unit tests deliberately cannot:
//
//   - RULE ORDER. `rules` is an order-significant list, but the object backend
//     stores rules keyed by id. If it returns them ordered by id rather than as
//     submitted, every plan shows a reordering diff. The rule ids below sort
//     differently (c, a, b) from their declared order on purpose.
//   - PREFIX ROUND TRIP. The service writes a prefix into the S3 lifecycle
//     rule's filter and reads it back from the same place. If the backend
//     echoes it somewhere else, a prefixed rule is a perpetual diff — and a
//     prefix silently lost widens what the rule deletes.
func TestAccBucketLifecycleConfiguration_basic(t *testing.T) {
	bucketName := acctest.RandomName("bucket")
	resourceName := "frostmoln_bucket_lifecycle_configuration.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLifecycleDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccLifecycleConfig(bucketName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "bucket", bucketName),
					resource.TestCheckResourceAttr(resourceName, "rules.#", "3"),
					// Declared order, not id order.
					resource.TestCheckResourceAttr(resourceName, "rules.0.id", "c-first"),
					resource.TestCheckResourceAttr(resourceName, "rules.1.id", "a-second"),
					resource.TestCheckResourceAttr(resourceName, "rules.2.id", "b-third"),
					resource.TestCheckResourceAttr(resourceName, "rules.0.prefix", "logs/"),
					resource.TestCheckResourceAttr(resourceName, "rules.1.prefix", "tmp/"),
					// Omitted optionals must read back as absent, not as zero.
					resource.TestCheckNoResourceAttr(resourceName, "rules.2.prefix"),
					resource.TestCheckNoResourceAttr(resourceName, "rules.2.expiration_days"),
				),
			},
			{
				Config: testAccLifecycleConfigUpdated(bucketName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "rules.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "rules.0.expiration_days", "90"),
					resource.TestCheckResourceAttr(resourceName, "rules.0.enabled", "false"),
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

// testAccCheckLifecycleDestroy asserts the configuration is gone. The bucket is
// destroyed alongside it, so a 404 on the sub-resource is the expected end
// state; an empty rule set is equally valid if the bucket outlives it.
func testAccCheckLifecycleDestroy(s *terraform.State) error {
	c, err := acctest.TestClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "frostmoln_bucket_lifecycle_configuration" {
			continue
		}

		bucket := rs.Primary.Attributes["bucket"]
		resp, err := c.Get(context.Background(), c.TenantPath("/buckets/"+bucket+"/lifecycle"), nil)
		if err != nil {
			if client.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("unexpected error reading lifecycle for %s: %w", bucket, err)
		}
		rules, err := client.ParseResponse[struct {
			Rules []map[string]any `json:"rules"`
		}](resp)
		if err != nil {
			return fmt.Errorf("parsing lifecycle for %s: %w", bucket, err)
		}
		if len(rules.Rules) != 0 {
			return fmt.Errorf("lifecycle configuration for bucket %s still has %d rule(s)", bucket, len(rules.Rules))
		}
	}

	return nil
}

func testAccLifecycleConfig(bucketName string) string {
	return fmt.Sprintf(`
resource "frostmoln_bucket" "test" {
  name = %[1]q
}

resource "frostmoln_bucket_lifecycle_configuration" "test" {
  bucket = frostmoln_bucket.test.name

  rules = [
    {
      id              = "c-first"
      enabled         = true
      prefix          = "logs/"
      expiration_days = 30
    },
    {
      id              = "a-second"
      enabled         = true
      prefix          = "tmp/"
      expiration_days = 7
    },
    {
      id                                     = "b-third"
      enabled                                = true
      abort_incomplete_multipart_upload_days = 3
    },
  ]
}
`, bucketName)
}

func testAccLifecycleConfigUpdated(bucketName string) string {
	return fmt.Sprintf(`
resource "frostmoln_bucket" "test" {
  name = %[1]q
}

resource "frostmoln_bucket_lifecycle_configuration" "test" {
  bucket = frostmoln_bucket.test.name

  rules = [
    {
      id              = "c-first"
      enabled         = false
      prefix          = "logs/"
      expiration_days = 90
    },
  ]
}
`, bucketName)
}
