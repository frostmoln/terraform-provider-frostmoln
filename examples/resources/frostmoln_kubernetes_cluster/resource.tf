data "frostmoln_kubernetes_versions" "available" {}

data "frostmoln_kubernetes_flavors" "available" {}

resource "frostmoln_kubernetes_cluster" "main" {
  name      = "my-cluster"
  version   = [for v in data.frostmoln_kubernetes_versions.available.versions : v.version if v.is_default][0]
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.nodes.id

  # Cluster addons are installed once, at creation, and cannot be changed on an
  # existing cluster (changing this set replaces the cluster). Omit the attribute
  # to install the platform defaults; set an empty list ([]) to install none.
  # See the frostmoln_kubernetes_addons data source for available keys.
  addons = ["external-secrets"]

  initial_node_pool = {
    flavor_id  = data.frostmoln_kubernetes_flavors.available.flavors[0].id
    node_count = 3
  }
}

output "cluster_endpoint" {
  value = frostmoln_kubernetes_cluster.main.endpoint
}

output "kubeconfig" {
  value     = frostmoln_kubernetes_cluster.main.kubeconfig
  sensitive = true
}
