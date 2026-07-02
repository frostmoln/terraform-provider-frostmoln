data "frostmoln_kubernetes_versions" "available" {}

# The platform default version
output "default_version" {
  value = [for v in data.frostmoln_kubernetes_versions.available.versions : v.version if v.is_default][0]
}
