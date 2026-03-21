package vpc_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

func TestAccDataSourceVPC_byName(t *testing.T) {
	name := acctest.RandomName("vpc")
	dataSourceName := "data.frostmoln_vpc.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDataSourceVPCConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(dataSourceName, "id"),
					resource.TestCheckResourceAttr(dataSourceName, "name", name),
					resource.TestCheckResourceAttr(dataSourceName, "cidr", "10.202.0.0/16"),
				),
			},
		},
	})
}

func testAccDataSourceVPCConfig(name string) string {
	return fmt.Sprintf(`
resource "frostmoln_vpc" "test" {
  name = %q
  cidr = "10.202.0.0/16"
}

data "frostmoln_vpc" "test" {
  name = frostmoln_vpc.test.name
}
`, name)
}
