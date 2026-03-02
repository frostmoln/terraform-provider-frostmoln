package flavors_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/acctest"
)

func TestAccDataSourceFlavors_list(t *testing.T) {
	dataSourceName := "data.frostmoln_flavors.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDataSourceFlavorsConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(dataSourceName, "flavors.#"),
				),
			},
		},
	})
}

func testAccDataSourceFlavorsConfig() string {
	return `
data "frostmoln_flavors" "test" {}
`
}
