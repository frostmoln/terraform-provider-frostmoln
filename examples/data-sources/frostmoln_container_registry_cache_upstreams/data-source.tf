# The upstreams a pull-through cache may front, and how many caches this tenant
# may hold.
#
# THIS IS THE ONLY AUTHORITATIVE COPY OF THAT CATALOG. It is server-owned: rows
# are added and retired without a provider release, so nothing published in
# documentation — and nothing hardcoded in a configuration — stays correct. The
# registry must already be enabled: a tenant that has not opted in gets an error
# here, not an empty catalog.
data "frostmoln_container_registry_cache_upstreams" "available" {}

output "upstream_keys" {
  value = [for u in data.frostmoln_container_registry_cache_upstreams.available.upstreams : u.key]
}

# Which upstreams demand your own credentials at that registry. Read the flag
# rather than special-casing a key: it is a property of the row, and the server
# enforces it either way.
output "upstreams_needing_credentials" {
  value = {
    for u in data.frostmoln_container_registry_cache_upstreams.available.upstreams :
    u.key => u.display if u.requires_credentials
  }
}

# The real cap, so a configuration is written against it instead of a number
# copied from documentation. Exceeding it is refused at apply time with a 403.
output "cache_limit" {
  value = data.frostmoln_container_registry_cache_upstreams.available.cache_limit
}
