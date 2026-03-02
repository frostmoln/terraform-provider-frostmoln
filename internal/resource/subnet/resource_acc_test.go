package subnet_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/acctest"
)

func TestAccSubnet_basic(t *testing.T) {
	vpcName := acctest.RandomName("vpc")
	subnetName := acctest.RandomName("subnet")
	resourceName := "frostmoln_subnet.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckDestroyByTenantPath("frostmoln_subnet", "/subnets"),
		Steps: []resource.TestStep{
			{
				Config: testAccSubnetConfig(vpcName, subnetName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", subnetName),
					resource.TestCheckResourceAttr(resourceName, "cidr", "10.101.1.0/24"),
					resource.TestCheckResourceAttrSet(resourceName, "vpc_id"),
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

func testAccSubnetConfig(vpcName, subnetName string) string {
	return fmt.Sprintf(`
resource "frostmoln_vpc" "test" {
  name = %q
  cidr = "10.101.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = %q
  cidr   = "10.101.1.0/24"
  vpc_id = frostmoln_vpc.test.id
}
`, vpcName, subnetName)
}
