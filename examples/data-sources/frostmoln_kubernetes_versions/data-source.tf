data "frostmoln_kubernetes_versions" "available" {}

# The platform default version
output "default_version" {
  value = [for v in data.frostmoln_kubernetes_versions.available.versions : v.version if v.is_default][0]
}

# Versions a new cluster can use: an eol version may still be returned, so filter on it
output "launchable_versions" {
  value = [
    for v in data.frostmoln_kubernetes_versions.available.versions : v.version
    if contains(["current", "supported", "deprecated"], v.status)
  ]
}
