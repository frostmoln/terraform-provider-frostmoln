data "frostmoln_kubernetes_tiers" "available" {}

# The default control-plane tier key
output "default_tier" {
  value = [for t in data.frostmoln_kubernetes_tiers.available.tiers : t.key if t.is_default][0]
}
