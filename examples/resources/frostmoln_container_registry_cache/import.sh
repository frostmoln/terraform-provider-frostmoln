# A cache is imported by its UPSTREAM KEY. There is no tenant segment: the
# operating tenant is a provider-level selector (the provider's `tenant_id`,
# FROSTMOLN_TENANT_ID, or your default tenant), so a tenant in the id would be
# ignored.
terraform import frostmoln_container_registry_cache.ghcr ghcr

# WARNING: the upstream credentials CANNOT be imported — no endpoint returns
# them. `username`, `password`, `password_wo` and `password_wo_version` all
# arrive unset, so a configuration that sets any of them will plan a REPLACE on
# the first apply after the import, which DELETES the cache and every image
# cached in it. For an upstream that needs no credentials the import is clean;
# for one that does (Docker Hub), expect that replace and schedule it.
