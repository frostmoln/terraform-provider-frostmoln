# Resolve a managed Kubernetes cluster that Terraform did not create
# (provisioned in the portal or with `fm`) by NAME — no hardcoded UUID.
data "frostmoln_kubernetes_cluster" "billing" {
  name = "billing-prod"
}

# The resolved cluster feeds non-secret references elsewhere — the API
# endpoint, the CIDRs, the region (the credential material itself is fetched
# separately by frostmoln_kubernetes_cluster_kubeconfig).
locals {
  apiserver_vip = data.frostmoln_kubernetes_cluster.billing.endpoint
  workloads_vpc = data.frostmoln_kubernetes_cluster.billing.vpc_id
}

# A lookup by id behaves identically for clusters whose UUID is already known.
data "frostmoln_kubernetes_cluster" "by_id" {
  id = data.frostmoln_kubernetes_cluster.billing.id
}

# A name that matches nothing — or more than one cluster — fails the read
# loudly rather than picking a cluster for you. A deleted cluster is reported
# as absent on both paths (deletes are soft: the deleted row answers 200
# forever, and this data source refuses to resolve it).

# The addons collection is NOT part of this data source: read the cluster's
# addon selection (and its applied versions) with
# frostmoln_kubernetes_cluster_addons.
