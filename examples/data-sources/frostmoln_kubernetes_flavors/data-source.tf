data "frostmoln_kubernetes_flavors" "available" {}

# Pick a node flavor by size
output "small_flavors" {
  value = [for f in data.frostmoln_kubernetes_flavors.available.flavors : f.id if f.vcpus == 2]
}
