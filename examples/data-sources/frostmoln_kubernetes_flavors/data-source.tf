data "frostmoln_kubernetes_flavors" "available" {}

# Pick a node flavor by the capacity you need, never by a hardcoded id: the
# catalogue is the source of truth for which sizes exist. The node-pool
# catalogue starts at `medium` — there is no `small` — so select on vcpus
# rather than assuming a size name.
output "two_vcpu_flavors" {
  value = [for f in data.frostmoln_kubernetes_flavors.available.flavors : f.id if f.vcpus == 2]
}
