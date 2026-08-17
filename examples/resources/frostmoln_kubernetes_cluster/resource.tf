data "frostmoln_kubernetes_versions" "available" {}

data "frostmoln_kubernetes_flavors" "available" {}

# The depends_on is not decoration. A cluster attaches public IPs into your VPC —
# the worker ingress load balancer when ingress_scheme is "public", and the API
# endpoint on accounts where that takes a public address — and it does so whether
# or not you name an address of your own, allocating one for you when you name
# none. Nothing here refers to frostmoln_gateway, so Terraform runs the two
# concurrently and on teardown the gateway can go first, with its delete refused
# (GATEWAY_IN_USE) part-way through the destroy.
#
# It costs more here than elsewhere: a cluster whose ingress load balancer cannot
# be built comes up WITHOUT one rather than failing the apply, so the damage is
# silent.
resource "frostmoln_kubernetes_cluster" "main" {
  name      = "my-cluster"
  version   = [for v in data.frostmoln_kubernetes_versions.available.versions : v.version if v.is_default][0]
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.nodes.id

  depends_on = [frostmoln_gateway.main]

  # Endpoint exposure. Omit for the default "public" (LB VIP reachable via a
  # public IP). Set "internal" for a private VIP-only endpoint reachable only
  # from inside the VPC, with no public IP allocated. Create-only: changing it
  # replaces the cluster. A bring-your-own address needs ingress_scheme = "public",
  # and must differ from public_ip_id (the two load balancers never share one).
  #
  # Your ingress controller Service must publish node ports 30080 (HTTP) and
  # 30443 (HTTPS); the load balancer forwards TCP 80 and 443 to them across every
  # worker node. Point DNS at ingress_endpoint, not at endpoint (that is kubectl's).
  # ingress_scheme       = "public"
  # ingress_public_ip_id = frostmoln_public_ip.ingress.id

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
