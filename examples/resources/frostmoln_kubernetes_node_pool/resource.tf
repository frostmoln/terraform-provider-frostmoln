data "frostmoln_kubernetes_flavors" "available" {}

# An additional node pool on an existing managed Kubernetes cluster. The
# cluster's initial pool is owned by the frostmoln_kubernetes_cluster resource
# (its initial_node_pool block) — this resource manages extra pools only.
resource "frostmoln_kubernetes_node_pool" "workers" {
  cluster_id = frostmoln_kubernetes_cluster.main.id
  name       = "workers" # optional — a "pool-<8 hex>" name is generated when omitted
  flavor_id  = data.frostmoln_kubernetes_flavors.available.flavors[0].id
  node_count = 2 # scaled in-place; name/flavor changes replace the pool
}
