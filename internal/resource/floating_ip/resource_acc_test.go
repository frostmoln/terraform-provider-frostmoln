package floating_ip_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/acctest"
)

func TestAccFloatingIP_basic(t *testing.T) {
	resourceName := "frostmoln_floating_ip.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckDestroyByTenantPath("frostmoln_floating_ip", "/floating-ips"),
		Steps: []resource.TestStep{
			{
				Config: testAccFloatingIPConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrSet(resourceName, "address"),
					resource.TestCheckResourceAttrSet(resourceName, "status"),
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

func testAccFloatingIPConfig() string {
	return `
resource "frostmoln_floating_ip" "test" {}
`
}
