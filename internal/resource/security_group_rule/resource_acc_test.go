package security_group_rule_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

func TestAccSecurityGroupRule_basic(t *testing.T) {
	sgName := acctest.RandomName("sg")
	resourceName := "frostmoln_security_group_rule.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheck(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckSecurityGroupRuleDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccSecurityGroupRuleConfig(sgName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttrSet(resourceName, "security_group_id"),
					resource.TestCheckResourceAttr(resourceName, "direction", "ingress"),
					resource.TestCheckResourceAttr(resourceName, "protocol", "tcp"),
					resource.TestCheckResourceAttr(resourceName, "port_range_min", "443"),
					resource.TestCheckResourceAttr(resourceName, "port_range_max", "443"),
					resource.TestCheckResourceAttr(resourceName, "remote_cidr", "0.0.0.0/0"),
				),
			},
		},
	})
}

func testAccCheckSecurityGroupRuleDestroy(s *terraform.State) error {
	// Just verify the parent security group is gone
	return acctest.CheckDestroyByTenantPath("frostmoln_security_group", "/security-groups")(s)
}

func testAccSecurityGroupRuleConfig(sgName string) string {
	return fmt.Sprintf(`
resource "frostmoln_security_group" "test" {
  name        = %q
  description = "Acceptance test SG"
}

resource "frostmoln_security_group_rule" "test" {
  security_group_id = frostmoln_security_group.test.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_cidr       = "0.0.0.0/0"
}
`, sgName)
}
