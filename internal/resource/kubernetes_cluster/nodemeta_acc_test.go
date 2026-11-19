package kubernetes_cluster_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
)

// TestAccKubernetesCluster_nodeMeta: node labels and taints on the initial pool (soft
// taints only) and on an extra pool (a NoSchedule taint), read back with no diff. The
// harness re-plans after each apply, so a read-back that disagreed would fail the step.
// Run it in a Frostmoln Test tenant; see the package comment in resource_acc_test.go.
func TestAccKubernetesCluster_nodeMeta(t *testing.T) {
	name := acctest.RandomName("k8s")
	vpcName := acctest.RandomName("vpc")
	subnetName := acctest.RandomName("subnet")
	clusterName := "frostmoln_kubernetes_cluster.test"
	poolName := "frostmoln_kubernetes_node_pool.dedicated"

	const pool = `
resource "frostmoln_kubernetes_node_pool" "dedicated" {
  cluster_id = frostmoln_kubernetes_cluster.test.id
  name       = "ded"
  flavor_id  = local.node_flavor
  node_count = 1
  labels     = { workload = "sensors" }
  taints     = [{ key = "dedicated", value = "sensors", effect = "NoSchedule" }]
}
`
	initialMeta := func(effect string) string {
		return `    labels     = { tier = "general" }
    taints     = [{ key = "soft", effect = "` + effect + `" }]
  }`
	}
	config := func(effect string) string {
		return strings.Replace(testAccKubernetesClusterConfig(vpcName, subnetName, name, 1, pool),
			"    node_count = 1\n  }", "    node_count = 1\n"+initialMeta(effect), 1)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheckKubernetes(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckKubernetesDestroyed,
		Steps: []resource.TestStep{
			{
				// Refused at plan time, before anything is created or destroyed.
				Config:      config("NoSchedule"),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`not accepted on the initial pool`),
			},
			{
				Config: config("PreferNoSchedule"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(clusterName, "initial_node_pool.labels.tier", "general"),
					resource.TestCheckResourceAttr(clusterName, "initial_node_pool.taints.#", "1"),
					resource.TestCheckResourceAttr(poolName, "labels.workload", "sensors"),
					resource.TestCheckResourceAttr(poolName, "taints.#", "1"),
					resource.TestCheckTypeSetElemNestedAttrs(poolName, "taints.*", map[string]string{
						"key": "dedicated", "value": "sensors", "effect": "NoSchedule",
					}),
				),
			},
			{
				ResourceName:      poolName,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs := s.RootModule().Resources[poolName]
					return rs.Primary.Attributes["cluster_id"] + "/" + rs.Primary.ID, nil
				},
				ImportStateVerifyIgnore: []string{"updated_at"},
			},
		},
	})
}
