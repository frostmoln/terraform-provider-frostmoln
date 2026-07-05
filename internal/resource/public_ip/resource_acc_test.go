package public_ip_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

func TestAccPublicIP_basic(t *testing.T) {
	resourceName := "frostmoln_public_ip.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckDestroyByTenantPath("frostmoln_public_ip", "/public-ips"),
		Steps: []resource.TestStep{
			{
				Config: testAccPublicIPConfig(),
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

func testAccPublicIPConfig() string {
	return `
resource "frostmoln_public_ip" "test" {}
`
}
