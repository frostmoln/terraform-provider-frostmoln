package images_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/acctest"
)

func TestAccDataSourceImages_list(t *testing.T) {
	dataSourceName := "data.frostmoln_images.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDataSourceImagesConfig(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(dataSourceName, "images.#"),
				),
			},
		},
	})
}

func testAccDataSourceImagesConfig() string {
	return `
data "frostmoln_images" "test" {}
`
}
