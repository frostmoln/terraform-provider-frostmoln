data "frostmoln_kubernetes_addons" "available" {}

# The catalog keys installed by default (when a cluster omits the addons attribute)
output "default_addons" {
  value = [for a in data.frostmoln_kubernetes_addons.available.addons : a.key if a.is_default]
}
