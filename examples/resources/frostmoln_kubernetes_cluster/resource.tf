data "frostmoln_kubernetes_versions" "available" {}

data "frostmoln_kubernetes_flavors" "available" {}

resource "frostmoln_kubernetes_cluster" "main" {
  name      = "my-cluster"
  version   = [for v in data.frostmoln_kubernetes_versions.available.versions : v.version if v.is_default][0]
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.nodes.id

  # To reach your workloads from outside the cluster, give your ingress controller
  # (or any workload) a Kubernetes Service of type LoadBalancer: the platform
  # provisions a load balancer per Service and reports its address in
  # .status.loadBalancer.ingress. The cluster itself provisions none — `endpoint`
  # below is the Kubernetes API endpoint, for kubectl, not for traffic.

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

# The API endpoint is PRIVATE: it lives on an address inside your own VPC and is
# reachable from inside that VPC only. A kubectl (or a `kubernetes`/`helm`
# provider, or a CI runner) outside the VPC cannot connect to it.
output "cluster_endpoint" {
  value = frostmoln_kubernetes_cluster.main.endpoint
}

output "kubeconfig" {
  value     = frostmoln_kubernetes_cluster.main.kubeconfig
  sensitive = true
}
