# Obey `launchable` rather than re-deriving it from `status`: the rule is the
# platform's, and a configuration that reimplements it is one lifecycle change
# away from selecting a version the server refuses.

data "frostmoln_appgw_versions" "available" {
  launchable_only = true
}

output "default_engine_version" {
  value = data.frostmoln_appgw_versions.available.default
}
