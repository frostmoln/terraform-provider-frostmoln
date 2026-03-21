package s3_credential_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

func TestAccS3Credential_basic(t *testing.T) {
	name := acctest.RandomName("s3cred")
	resourceName := "frostmoln_s3_credential.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckDestroyByTenantPath("frostmoln_s3_credential", "/credentials"),
		Steps: []resource.TestStep{
			{
				Config: testAccS3CredentialConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttrSet(resourceName, "secret_access_key"),
				),
			},
		},
	})
}

func testAccS3CredentialConfig(name string) string {
	return fmt.Sprintf(`
resource "frostmoln_s3_credential" "test" {
  name = %q
}
`, name)
}
