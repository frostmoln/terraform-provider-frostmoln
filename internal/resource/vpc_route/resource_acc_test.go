package vpc_route_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

func TestAccVPCRoute_basic(t *testing.T) {
	vpcName := acctest.RandomName("vpc")
	resourceName := "frostmoln_vpc_route.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckVPCRouteDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccVPCRouteConfig(vpcName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrSet(resourceName, "vpc_id"),
					resource.TestCheckResourceAttr(resourceName, "destination", "203.0.113.0/24"),
					resource.TestCheckResourceAttr(resourceName, "next_hop", "10.0.1.10"),
				),
			},
			{
				// The import id carries a CIDR, so it contains a slash of its
				// own — this step is what proves the round trip survives it.
				ResourceName:      resourceName,
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func testAccCheckVPCRouteDestroy(s *terraform.State) error {
	// A route has no addressable resource of its own; verifying the parent VPC
	// is gone is what is checkable from here, exactly as for a security group
	// rule.
	return acctest.CheckDestroyByTenantPath("frostmoln_vpc", "/vpcs")(s)
}

func testAccVPCRouteConfig(vpcName string) string {
	return fmt.Sprintf(`
resource "frostmoln_vpc" "test" {
  name = %q
  cidr = "10.0.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = "acc-route-subnet"
  cidr   = "10.0.1.0/24"
  vpc_id = frostmoln_vpc.test.id
  zone   = "falkenberg"
}

resource "frostmoln_vpc_route" "test" {
  vpc_id      = frostmoln_vpc.test.id
  destination = "203.0.113.0/24"
  next_hop    = "10.0.1.10"

  depends_on = [frostmoln_subnet.test]
}
`, vpcName)
}
