package ssh_key_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

const testAccSSHKeyPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGwf2tOoGfmCSSeKYxFSS/JRbMbz5BJ5A9xVyXc5Oc/n acctest@test"

func TestAccSSHKey_basic(t *testing.T) {
	name := acctest.RandomName("sshkey")
	resourceName := "frostmoln_ssh_key.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckDestroyByTenantPath("frostmoln_ssh_key", "/sshkeys"),
		Steps: []resource.TestStep{
			{
				Config: testAccSSHKeyConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttrSet(resourceName, "fingerprint"),
					resource.TestCheckResourceAttr(resourceName, "public_key", testAccSSHKeyPublicKey),
				),
			},
			{
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccSSHKeyConfig(name string) string {
	return fmt.Sprintf(`
resource "frostmoln_ssh_key" "test" {
  name       = %q
  public_key = %q
}
`, name, testAccSSHKeyPublicKey)
}
