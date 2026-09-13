data "frostmoln_kubernetes_addon_versions" "external_secrets" {
  addon = "external-secrets"
}

# The recommended version, taken from is_default. The list is in the platform's
# order; never sort it or compare version strings to find the newest.
output "recommended_external_secrets_version" {
  value = one([for v in data.frostmoln_kubernetes_addon_versions.external_secrets.versions : v.version if v.is_default])
}
