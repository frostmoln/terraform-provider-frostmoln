package regions_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

func TestAccDataSourceRegions_list(t *testing.T) {
	dataSourceName := "data.frostmoln_regions.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDataSourceRegionsConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(dataSourceName, "regions.#"),
				),
			},
		},
	})
}

func testAccDataSourceRegionsConfig() string {
	return `
data "frostmoln_regions" "test" {}
`
}
