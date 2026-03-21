package subnet_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

func TestAccDataSourceSubnet_byID(t *testing.T) {
	vpcName := acctest.RandomName("vpc")
	subnetName := acctest.RandomName("subnet")
	dataSourceName := "data.frostmoln_subnet.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDataSourceSubnetConfig(vpcName, subnetName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair(dataSourceName, "id", "frostmoln_subnet.test", "id"),
					resource.TestCheckResourceAttr(dataSourceName, "name", subnetName),
					resource.TestCheckResourceAttr(dataSourceName, "cidr", "10.203.1.0/24"),
					resource.TestCheckResourceAttrSet(dataSourceName, "vpc_id"),
				),
			},
		},
	})
}

func testAccDataSourceSubnetConfig(vpcName, subnetName string) string {
	return fmt.Sprintf(`
resource "frostmoln_vpc" "test" {
  name = %q
  cidr = "10.203.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = %q
  cidr   = "10.203.1.0/24"
  vpc_id = frostmoln_vpc.test.id
}

data "frostmoln_subnet" "test" {
  id = frostmoln_subnet.test.id
}
`, vpcName, subnetName)
}
