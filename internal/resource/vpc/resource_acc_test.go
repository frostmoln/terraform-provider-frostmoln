package vpc_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"git.nl.cloud/NordicLight/terraform-provider-frostmoln/internal/acctest"
)

func TestAccVPC_basic(t *testing.T) {
	name := acctest.RandomName("vpc")
	nameUpdated := acctest.RandomName("vpc")
	resourceName := "frostmoln_vpc.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             acctest.CheckDestroyByTenantPath("frostmoln_vpc", "/vpcs"),
		Steps: []resource.TestStep{
			{
				Config: testAccVPCConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttr(resourceName, "cidr", "10.100.0.0/16"),
					resource.TestCheckResourceAttrSet(resourceName, "status"),
				),
			},
			{
				Config: testAccVPCConfig(nameUpdated),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", nameUpdated),
					resource.TestCheckResourceAttr(resourceName, "cidr", "10.100.0.0/16"),
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

func testAccVPCConfig(name string) string {
	return fmt.Sprintf(`
resource "frostmoln_vpc" "test" {
  name = %q
  cidr = "10.100.0.0/16"
}
`, name)
}
