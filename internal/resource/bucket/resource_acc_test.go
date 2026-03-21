package bucket_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

func TestAccBucket_basic(t *testing.T) {
	name := acctest.RandomName("bucket")
	resourceName := "frostmoln_bucket.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckBucketDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccBucketConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttrSet(resourceName, "endpoint"),
				),
			},
			{
				Config: testAccBucketConfigVersioning(name, "enabled"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttr(resourceName, "versioning", "enabled"),
					resource.TestCheckResourceAttrSet(resourceName, "endpoint"),
				),
			},
			{
				ResourceName:            resourceName,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"access_key"},
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources[resourceName]
					if !ok {
						return "", fmt.Errorf("resource %s not found", resourceName)
					}
					return rs.Primary.Attributes["name"], nil
				},
			},
		},
	})
}

func testAccCheckBucketDestroy(s *terraform.State) error {
	c, err := acctest.TestClient()
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "frostmoln_bucket" {
			continue
		}

		name := rs.Primary.Attributes["name"]
		_, err := c.Get(context.Background(), c.TenantPath("/buckets/"+name), nil)
		if err == nil {
			return fmt.Errorf("bucket %s still exists", name)
		}
		if !client.IsNotFound(err) {
			return fmt.Errorf("unexpected error: %s", err)
		}
	}

	return nil
}

func testAccBucketConfig(name string) string {
	return fmt.Sprintf(`
resource "frostmoln_bucket" "test" {
  name = %q
}
`, name)
}

func testAccBucketConfigVersioning(name, versioning string) string {
	return fmt.Sprintf(`
resource "frostmoln_bucket" "test" {
  name       = %q
  versioning = %q
}
`, name, versioning)
}
