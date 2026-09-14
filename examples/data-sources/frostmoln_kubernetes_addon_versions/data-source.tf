data "frostmoln_kubernetes_addon_versions" "external_dns" {
  addon = "external-dns"
}

# The recommended version, taken from is_default. The list is in the platform's
# order; never sort it or compare version strings to find the newest.
output "recommended_external_dns_version" {
  value = one([for v in data.frostmoln_kubernetes_addon_versions.external_dns.versions : v.version if v.is_default])
}
