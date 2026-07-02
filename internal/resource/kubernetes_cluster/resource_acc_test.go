package kubernetes_cluster_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/acctest"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// These tests also cover frostmoln_kubernetes_node_pool: an extra pool only
// exists on a live cluster, so pooling both resources into one cluster
// lifecycle avoids a second ~multi-minute cluster create per package.
//
// They create REAL, BILLED clusters, so they are double-gated (see
// acctest.TestAccPreCheckKubernetes): an explicit FROSTMOLN_TEST_KUBERNETES=1
// opt-in AND a tenant holding the `kubernetes` entitlement (ADR-0038). The CI
// acceptance tenant is created fresh per run, is NOT entitled, and does not
// set the opt-in — these tests always skip there.
//
// Running them for real (entitled tenant, API key with kubernetes:read +
// kubernetes:write + network:read + network:write scopes):
//
//	FROSTMOLN_API_ENDPOINT=<api> FROSTMOLN_API_KEY=<key> \
//	  FROSTMOLN_TEST_KUBERNETES=1 TF_ACC=1 \
//	  go test -v -count=1 -timeout 45m -run 'TestAccKubernetesCluster' \
//	  ./internal/resource/kubernetes_cluster/...
//
// Run this package ISOLATED (not `./internal/...`): the cluster's node VMs
// race the other acceptance packages for the same tenant quota. The -timeout
// must comfortably exceed BOTH cluster lifecycles (~5-10 min total live): a
// go-test deadline kill skips destroy AND CheckDestroy, orphaning a billing
// cluster + nodes + VPC. Never entitle the CI acceptance tenant without also
// raising the workflow's shared 30m go-test timeout.

// TestAccKubernetesCluster_full exercises the whole plan-Phase-5 flow on one
// cluster: create (catalog data sources, no hardcoded version/flavor/tier) →
// implicit empty re-plan → rename + scale the initial pool + add an extra
// frostmoln_kubernetes_node_pool → import the cluster → import the pool →
// destroy, with CheckDestroy asserting the SOFT-deleted rows report
// status "deleted" (the backend never 404s a deleted cluster/pool).
func TestAccKubernetesCluster_full(t *testing.T) {
	name := acctest.RandomName("k8s")
	renamed := name + "-renamed"
	vpcName := acctest.RandomName("vpc")
	subnetName := acctest.RandomName("subnet")
	resourceName := "frostmoln_kubernetes_cluster.test"
	poolResourceName := "frostmoln_kubernetes_node_pool.extra"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheckKubernetes(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckKubernetesDestroyed,
		Steps: []resource.TestStep{
			{
				Config: testAccKubernetesClusterConfig(vpcName, subnetName, name, 1, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(resourceName, "id"),
					resource.TestCheckResourceAttr(resourceName, "name", name),
					resource.TestCheckResourceAttr(resourceName, "status", "running"),
					resource.TestCheckResourceAttrSet(resourceName, "version"),
					resource.TestCheckResourceAttrSet(resourceName, "control_plane_tier"),
					resource.TestCheckResourceAttr(resourceName, "initial_node_pool.name", "default"),
					resource.TestCheckResourceAttr(resourceName, "initial_node_pool.node_count", "1"),
					resource.TestCheckResourceAttr(resourceName, "initial_node_pool.status", "active"),
					resource.TestCheckResourceAttrSet(resourceName, "initial_node_pool.id"),
					resource.TestCheckResourceAttrSet(resourceName, "endpoint"),
					resource.TestCheckResourceAttrSet(resourceName, "floating_ip"),
					resource.TestCheckResourceAttrSet(resourceName, "load_balancer_id"),
					resource.TestCheckResourceAttrSet(resourceName, "pod_cidr"),
					resource.TestCheckResourceAttrSet(resourceName, "service_cidr"),
					resource.TestCheckResourceAttrSet(resourceName, "ca_cert_hash"),
					resource.TestCheckResourceAttrSet(resourceName, "tenant_id"),
					// The three catalog data sources feeding the config are
					// implicitly asserted non-empty by the locals' [0]
					// indexing; addons is only read, so assert it explicitly
					// (".#" is "set" even for an empty list).
					resource.TestCheckResourceAttrWith("data.frostmoln_kubernetes_addons.all", "addons.#",
						func(v string) error {
							if v == "0" {
								return fmt.Errorf("addons catalog is empty")
							}
							return nil
						}),
					testAccCheckKubeconfigServesEndpoint(resourceName),
				),
			},
			{
				// Rename in-place, scale the initial pool in-place, and attach
				// an extra node pool — the pool resource's create path.
				Config: testAccKubernetesClusterConfig(vpcName, subnetName, renamed, 2, testAccExtraNodePool),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "name", renamed),
					resource.TestCheckResourceAttr(resourceName, "status", "running"),
					resource.TestCheckResourceAttr(resourceName, "initial_node_pool.node_count", "2"),
					resource.TestCheckResourceAttr(resourceName, "initial_node_pool.status", "active"),
					resource.TestCheckResourceAttrSet(poolResourceName, "id"),
					resource.TestCheckResourceAttr(poolResourceName, "name", "extra"),
					resource.TestCheckResourceAttr(poolResourceName, "node_count", "1"),
					resource.TestCheckResourceAttr(poolResourceName, "status", "active"),
					resource.TestCheckResourceAttrPair(poolResourceName, "cluster_id", resourceName, "id"),
				),
			},
			{
				ResourceName: resourceName,
				ImportState:  true,
				// kubeconfig IS verified: the backend serves a static
				// Vault-stored bundle, so the import-time re-fetch matches the
				// create-time one. If kubeconfigs ever become minted/short-
				// lived, move it into ImportStateVerifyIgnore.
				ImportStateVerify: true,
				// updated_at moves under the test's feet (the kubernetes
				// service Syncer and event consumers touch the row async).
				ImportStateVerifyIgnore: []string{"updated_at"},
			},
			{
				ResourceName:      poolResourceName,
				ImportState:       true,
				ImportStateVerify: true,
				// Import ID format: {cluster_id}/{pool_id}.
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					rs, ok := s.RootModule().Resources[poolResourceName]
					if !ok {
						return "", fmt.Errorf("resource %s not found in state", poolResourceName)
					}
					return rs.Primary.Attributes["cluster_id"] + "/" + rs.Primary.ID, nil
				},
				ImportStateVerifyIgnore: []string{"updated_at"},
			},
		},
	})
}

// TestAccKubernetesCluster_byoFloatingIP creates a cluster on a
// bring-your-own floating IP, then removes only the cluster from the config
// and verifies the FIP SURVIVES the cluster deletion: the plan must treat the
// FIP as a no-op (a saga that wrongly released it would refresh to 404 and
// plan a re-create), and an out-of-band API read must return the same
// address. Re-association was proven in the live BYO-FIP acceptance (Ambix
// 019ecab4-6125); re-proving it here would cost a second cluster create.
func TestAccKubernetesCluster_byoFloatingIP(t *testing.T) {
	name := acctest.RandomName("k8s-byo")
	vpcName := acctest.RandomName("vpc")
	subnetName := acctest.RandomName("subnet")
	resourceName := "frostmoln_kubernetes_cluster.test"
	fipResourceName := "frostmoln_floating_ip.byo"

	// Captured in step 1 so step 2 can verify — out of band — that the
	// cluster is gone and the exact same FIP survived after the cluster left
	// the Terraform state.
	var clusterID, fipID, fipAddress string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.TestAccPreCheckKubernetes(t) },
		ProtoV6ProviderFactories: acctest.TestAccProtoV6ProviderFactories,
		CheckDestroy: resource.ComposeTestCheckFunc(
			testAccCheckKubernetesDestroyed,
			// Network FIPs hard-delete (404), unlike the soft-deleted K8s
			// rows — but the release is async (202 fire-and-forget), so poll
			// instead of the immediate acctest.CheckDestroyByTenantPath.
			checkFloatingIPsReleased,
		),
		Steps: []resource.TestStep{
			{
				Config: testAccKubernetesClusterBYOFIPConfig(vpcName, subnetName, name, true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(resourceName, "status", "running"),
					resource.TestCheckResourceAttrPair(resourceName, "floating_ip_id", fipResourceName, "id"),
					resource.TestCheckResourceAttrPair(resourceName, "floating_ip", fipResourceName, "address"),
					func(s *terraform.State) error {
						cl, ok := s.RootModule().Resources[resourceName]
						if !ok {
							return fmt.Errorf("resource %s not found in state", resourceName)
						}
						clusterID = cl.Primary.ID
						fip, ok := s.RootModule().Resources[fipResourceName]
						if !ok {
							return fmt.Errorf("resource %s not found in state", fipResourceName)
						}
						fipID = fip.Primary.ID
						fipAddress = fip.Primary.Attributes["address"]
						return nil
					},
				),
			},
			{
				// Same config minus the cluster: destroys the cluster only.
				Config: testAccKubernetesClusterBYOFIPConfig(vpcName, subnetName, name, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						// A FIP the cluster delete wrongly released would
						// refresh to 404 and plan as a CREATE here — the
						// survival assertion must fail loudly, not silently
						// re-create.
						plancheck.ExpectResourceAction(fipResourceName, plancheck.ResourceActionNoop),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					func(*terraform.State) error {
						if clusterID == "" || fipID == "" {
							return fmt.Errorf("cluster/FIP IDs were not captured in step 1")
						}
						c, err := acctest.TestClient()
						if err != nil {
							return err
						}
						if err := checkSoftDeleted(c, "kubernetes cluster "+clusterID,
							c.TenantPath("/kubernetes-clusters/"+clusterID)); err != nil {
							return err
						}
						// The very same FIP still exists with its address.
						resp, err := c.Get(context.Background(), c.TenantPath("/floating-ips/"+fipID), nil)
						if err != nil {
							return fmt.Errorf("BYO floating IP %s did not survive the cluster delete: %w", fipID, err)
						}
						fip, err := client.ParseResponse[struct {
							Address string `json:"floatingIpAddress"`
						}](resp)
						if err != nil {
							return fmt.Errorf("failed to parse floating IP %s: %w", fipID, err)
						}
						if fip.Address != fipAddress {
							return fmt.Errorf("BYO floating IP %s changed address: had %s, now %s", fipID, fipAddress, fip.Address)
						}
						return nil
					},
				),
			},
		},
	})
}

// testAccCheckKubeconfigServesEndpoint verifies the kubeconfig output is the
// real admin bundle pointing at the cluster's API endpoint. The embedded-
// credential markers matter: the backend's Vault-not-wired fallback serves a
// placeholder skeleton that still contains clusters:/users: sections but no
// certificate data.
func testAccCheckKubeconfigServesEndpoint(resourceName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("resource %s not found in state", resourceName)
		}
		kubeconfig := rs.Primary.Attributes["kubeconfig"]
		endpoint := rs.Primary.Attributes["endpoint"]
		if kubeconfig == "" {
			return fmt.Errorf("kubeconfig is empty")
		}
		if endpoint == "" {
			return fmt.Errorf("endpoint is empty")
		}
		for _, marker := range []string{"clusters:", "users:", "certificate-authority-data:", "client-certificate-data:"} {
			if !strings.Contains(kubeconfig, marker) {
				return fmt.Errorf("kubeconfig is missing %q — not a usable admin bundle", marker)
			}
		}
		if !strings.Contains(kubeconfig, endpoint) {
			return fmt.Errorf("kubeconfig does not reference the cluster endpoint %s", endpoint)
		}
		return nil
	}
}

// apiStatus is the minimal response shape for out-of-band status checks.
type apiStatus struct {
	Status string `json:"status"`
}

// checkSoftDeleted asserts the object at path reports status "deleted".
// Deletes are SOFT: the row is retained and GET keeps answering 200 with
// status "deleted" forever — a 404 is accepted only as a fallback.
func checkSoftDeleted(c *client.Client, what, path string) error {
	resp, err := c.Get(context.Background(), path, nil)
	if err != nil {
		if client.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("unexpected error checking %s: %w", what, err)
	}
	parsed, err := client.ParseResponse[apiStatus](resp)
	if err != nil {
		return fmt.Errorf("failed to parse %s status: %w", what, err)
	}
	if parsed.Status != "deleted" {
		return fmt.Errorf("%s still exists with status %q", what, parsed.Status)
	}
	return nil
}

// checkFloatingIPsReleased verifies every floating IP left in state is gone
// (404). The FIP resource's Delete is async fire-and-forget (202), so the
// allocation lingers briefly after destroy returns — poll before declaring a
// leak.
func checkFloatingIPsReleased(s *terraform.State) error {
	c, err := acctest.TestClient()
	if err != nil {
		return err
	}
	for name, rs := range s.RootModule().Resources {
		if rs.Type != "frostmoln_floating_ip" {
			continue
		}
		deadline := time.Now().Add(90 * time.Second)
		for {
			_, err := c.Get(context.Background(), c.TenantPath("/floating-ips/"+rs.Primary.ID), nil)
			if client.IsNotFound(err) {
				break
			}
			if err != nil {
				return fmt.Errorf("unexpected error checking %s (%s): %w", name, rs.Primary.ID, err)
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("floating IP %s (%s) still exists 90s after destroy", name, rs.Primary.ID)
			}
			time.Sleep(5 * time.Second)
		}
	}
	return nil
}

// testAccCheckKubernetesDestroyed verifies every cluster and node pool left
// in state is soft-deleted (status "deleted") after destroy — no orphaned
// clusters, pools, or their underlying nodes keep billing.
func testAccCheckKubernetesDestroyed(s *terraform.State) error {
	c, err := acctest.TestClient()
	if err != nil {
		return err
	}
	for name, rs := range s.RootModule().Resources {
		switch rs.Type {
		case "frostmoln_kubernetes_cluster":
			if err := checkSoftDeleted(c, name, c.TenantPath("/kubernetes-clusters/"+rs.Primary.ID)); err != nil {
				return err
			}
		case "frostmoln_kubernetes_node_pool":
			path := c.TenantPath("/kubernetes-clusters/" + rs.Primary.Attributes["cluster_id"] + "/node-pools/" + rs.Primary.ID)
			if err := checkSoftDeleted(c, name, path); err != nil {
				return err
			}
		}
	}
	return nil
}

// testAccExtraNodePool is appended to the cluster config in the step that
// exercises the frostmoln_kubernetes_node_pool resource.
const testAccExtraNodePool = `
resource "frostmoln_kubernetes_node_pool" "extra" {
  cluster_id = frostmoln_kubernetes_cluster.test.id
  name       = "extra"
  flavor_id  = local.node_flavor
  node_count = 1
}
`

// testAccNodeFlavorLocals picks the SMALLEST node flavor by vCPU count so the
// test's cost and quota footprint stays bounded regardless of catalog order.
const testAccNodeFlavorLocals = `
locals {
  min_vcpus   = min([for f in data.frostmoln_kubernetes_flavors.all.flavors : f.vcpus]...)
  node_flavor = [for f in data.frostmoln_kubernetes_flavors.all.flavors : f.id if f.vcpus == local.min_vcpus][0]
}
`

// testAccKubernetesClusterConfig renders a cluster + prerequisites, sourcing
// version, tier, and flavor from the catalog data sources (never hardcoded —
// the catalog is the source of truth, ADR-0058/ADR-0015 spirit). The VPC CIDR
// stays clear of the reserved K8s pod (10.160.0.0/11) and service
// (10.224.0.0/12) pools, which the network service rejects.
func testAccKubernetesClusterConfig(vpcName, subnetName, clusterName string, initialNodeCount int, extra string) string {
	return fmt.Sprintf(`
data "frostmoln_kubernetes_versions" "all" {}
data "frostmoln_kubernetes_tiers" "all" {}
data "frostmoln_kubernetes_flavors" "all" {}
data "frostmoln_kubernetes_addons" "all" {}

locals {
  default_version = [for v in data.frostmoln_kubernetes_versions.all.versions : v.version if v.is_default][0]
  default_tier    = [for t in data.frostmoln_kubernetes_tiers.all.tiers : t.key if t.is_default][0]
}
%[6]s

resource "frostmoln_vpc" "test" {
  name = %[1]q
  cidr = "10.42.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = %[2]q
  vpc_id = frostmoln_vpc.test.id
  cidr   = "10.42.1.0/24"
}

resource "frostmoln_kubernetes_cluster" "test" {
  name               = %[3]q
  version            = local.default_version
  control_plane_tier = local.default_tier
  vpc_id             = frostmoln_vpc.test.id
  subnet_id          = frostmoln_subnet.test.id

  initial_node_pool = {
    flavor_id  = local.node_flavor
    node_count = %[4]d
  }
}
%[5]s`, vpcName, subnetName, clusterName, initialNodeCount, extra, testAccNodeFlavorLocals)
}

// testAccKubernetesClusterBYOFIPConfig renders a standalone floating IP and,
// when withCluster is true, a cluster using it as the API endpoint FIP.
func testAccKubernetesClusterBYOFIPConfig(vpcName, subnetName, clusterName string, withCluster bool) string {
	cluster := ""
	if withCluster {
		cluster = fmt.Sprintf(`
resource "frostmoln_kubernetes_cluster" "test" {
  name           = %[1]q
  vpc_id         = frostmoln_vpc.test.id
  subnet_id      = frostmoln_subnet.test.id
  floating_ip_id = frostmoln_floating_ip.byo.id

  initial_node_pool = {
    flavor_id  = local.node_flavor
    node_count = 1
  }
}
`, clusterName)
	}

	return fmt.Sprintf(`
data "frostmoln_kubernetes_flavors" "all" {}
%[4]s

resource "frostmoln_vpc" "test" {
  name = %[1]q
  cidr = "10.43.0.0/16"
}

resource "frostmoln_subnet" "test" {
  name   = %[2]q
  vpc_id = frostmoln_vpc.test.id
  cidr   = "10.43.1.0/24"
}

resource "frostmoln_floating_ip" "byo" {}
%[3]s`, vpcName, subnetName, cluster, testAccNodeFlavorLocals)
}
