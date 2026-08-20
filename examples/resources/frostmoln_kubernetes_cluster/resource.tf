data "frostmoln_kubernetes_versions" "available" {}

data "frostmoln_kubernetes_flavors" "available" {}

# The depends_on is not decoration. A cluster attaches a public IP into your VPC
# for its API endpoint, on accounts where that endpoint takes one — and it does so
# whether or not you name an address of your own, allocating one for you when you
# name none. Nothing here refers to frostmoln_gateway, so Terraform runs the two
# concurrently and on teardown the gateway can go first, with its delete refused
# (GATEWAY_IN_USE) part-way through the destroy.
resource "frostmoln_kubernetes_cluster" "main" {
  name      = "my-cluster"
  version   = [for v in data.frostmoln_kubernetes_versions.available.versions : v.version if v.is_default][0]
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.nodes.id

  depends_on = [frostmoln_gateway.main]

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

output "cluster_endpoint" {
  value = frostmoln_kubernetes_cluster.main.endpoint
}

output "kubeconfig" {
  value     = frostmoln_kubernetes_cluster.main.kubeconfig
  sensitive = true
}
