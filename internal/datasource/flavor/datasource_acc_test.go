package flavor_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/acctest"
)

func TestAccDataSourceFlavor_byName(t *testing.T) {
	flavorName := acctest.TestFlavorName()
	dataSourceName := "data.frostmoln_flavor.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDataSourceFlavorConfig(flavorName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(dataSourceName, "id"),
					resource.TestCheckResourceAttr(dataSourceName, "name", flavorName),
					resource.TestCheckResourceAttrSet(dataSourceName, "vcpus"),
					resource.TestCheckResourceAttrSet(dataSourceName, "ram_mb"),
				),
			},
		},
	})
}

func testAccDataSourceFlavorConfig(name string) string {
	return fmt.Sprintf(`
data "frostmoln_flavor" "test" {
  name = %q
}
`, name)
}
