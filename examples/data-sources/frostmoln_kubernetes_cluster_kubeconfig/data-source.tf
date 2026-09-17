# Resolve a managed Kubernetes cluster by NAME and fetch its kubeconfig —
# no hardcoded UUID (a portal- or `fm`-created cluster is otherwise reachable
# only by its id).
data "frostmoln_kubernetes_cluster_kubeconfig" "billing" {
  name = "billing-prod"
}

# The blob is SECRET material: it carries the cluster client's private key.
# It appears redacted in CLI output, but it still lands in Terraform state —
# protect that state like you would protect a kubeconfig file (the non-secret
# pairing, the CA cert hash, lives on frostmoln_kubernetes_cluster).

# Composition contract: hand the raw blob to a provider's kubeconfig input.
# No parsing of the client key/cert out of the YAML happens here — the raw
# string is the contract every kube client composes from. Commented rather
# than live so this example does not read like a state fixture holding fake
# credentials:
#
# provider "kubernetes" {
#   host = data.frostmoln_kubernetes_cluster_kubeconfig.billing.endpoint
#   # the kubernetes provider accepts the kubeconfig as raw YAML
# }
#
# provider "helm" {
#   # the helm provider takes the same raw blob
# }

# A cluster that matches nothing, an ambiguous name, or a soft-deleted cluster
# fails the read. So does an EMPTY kubeconfig on a 200: the cluster may not be
# serviceable yet, and empty credential material is never rendered silently.
