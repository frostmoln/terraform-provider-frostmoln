package load_balancer_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// TestAccLoadBalancer_basic provisions a full load balancer stack: an amphora
// load balancer, an HTTPS listener with an explicit allowed_cidrs allow-list, a
// backend pool, a member, and a health monitor.
func TestAccLoadBalancer_basic(t *testing.T) {
	name := acctest.RandomName("lb")
	vpcName := acctest.RandomName("vpc")
	subnetName := acctest.RandomName("subnet")
	resourceName := "frostmoln_load_balancer.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckLoadBalancerDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccLoadBalancerConfig(name, vpcName, subnetName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttr(resourceName, "provider_type", "amphora"),
					resource.TestCheckResourceAttrSet(resourceName, "vip_address"),
					resource.TestCheckResourceAttr("frostmoln_lb_listener.test", "protocol", "https"),
					resource.TestCheckResourceAttr("frostmoln_lb_pool.test", "lb_algorithm", "round_robin"),
					resource.TestCheckResourceAttr("frostmoln_lb_health_monitor.test", "type", "http"),
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

func testAccCheckLoadBalancerDestroy(s *terraform.State) error {
	return acctest.CheckDestroyByTenantPath("frostmoln_load_balancer", "/load-balancers")(s)
}

func testAccLoadBalancerConfig(name, vpcName, subnetName string) string {
	return fmt.Sprintf(`
resource "frostmoln_vpc" "test" {
  name = %q
  cidr = "10.10.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = %q
  vpc_id = frostmoln_vpc.test.id
  cidr   = "10.10.1.0/24"
}

# provider and flavor_id are ForceNew. There is no in-place migration between
# the amphora and ovn Octavia drivers; switching them destroys and recreates.
resource "frostmoln_load_balancer" "test" {
  name          = %q
  vpc_id        = frostmoln_vpc.test.id
  subnet_id     = frostmoln_subnet.test.id
  provider_type = "amphora"
}

resource "frostmoln_lb_listener" "test" {
  load_balancer_id = frostmoln_load_balancer.test.id
  name             = "https"
  protocol         = "https"
  protocol_port    = 443
  # Deny-by-default: allow-all must be explicit.
  allowed_cidrs = ["0.0.0.0/0"]
}

resource "frostmoln_lb_pool" "test" {
  load_balancer_id = frostmoln_load_balancer.test.id
  listener_id      = frostmoln_lb_listener.test.id
  name             = "backend"
  protocol         = "http"
  lb_algorithm     = "round_robin"
}

resource "frostmoln_lb_member" "test" {
  load_balancer_id = frostmoln_load_balancer.test.id
  pool_id          = frostmoln_lb_pool.test.id
  address          = "10.10.1.20"
  protocol_port    = 8080
  weight           = 1
}

resource "frostmoln_lb_health_monitor" "test" {
  load_balancer_id = frostmoln_load_balancer.test.id
  pool_id          = frostmoln_lb_pool.test.id
  type             = "http"
  url_path         = "/healthz"
  expected_codes   = "200"
}
`, vpcName, subnetName, name)
}
