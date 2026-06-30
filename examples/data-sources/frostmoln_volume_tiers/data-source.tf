data "frostmoln_volume_tiers" "all" {}

# The tier keys that may be used as a volume's volume_type (offered only).
output "offered_volume_tier_keys" {
  value = [for t in data.frostmoln_volume_tiers.all.volume_tiers : t.key if t.status == "offered"]
}
