data "frostmoln_kubernetes_versions" "available" {}

variable "external_dns_version" {
  description = "A version of external-dns listed by the frostmoln_kubernetes_addon_versions data source."
  type        = string
}

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

  # Cluster addons. Adding OR removing a key is applied in place to a running
  # cluster and reaches it within the platform's addon reconciliation period;
  # removing one DELETES the objects that addon installed. Omit the attribute to
  # install the platform defaults; set an empty list ([]) to install none. See the
  # frostmoln_kubernetes_addons data source for available keys.
  #
  # external-dns requires the tenant's DNS feature, and publishes nothing until you
  # create its `frostmoln-dns` API-key Secret inside the cluster. Create that Secret
  # outside Terraform (kubectl), with an API key carrying the narrowest scopes that
  # allow DNS record changes (list the available scopes with the
  # frostmoln_api_key_scopes data source). Creating it
  # with Terraform instead puts the key in plain text in Terraform state, so
  # protect the state backend accordingly.
  addons = ["external-dns"]

  # Optional version pins, keyed by addon key; each key must be in addons. Use a
  # version string from the frostmoln_kubernetes_addon_versions data source. Pins
  # set here at creation go in the create request; after that, only a changed pin
  # is sent, and removing a key leaves the addon on its version.
  addon_versions = {
    "external-dns" = var.external_dns_version
  }

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
