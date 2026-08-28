# Read the tenant's registry WITHOUT creating one.
#
# The frostmoln_container_registry RESOURCE is the opt-in: against a tenant that
# has not opted in it creates the registry and starts its billable storage. Use
# this data source when the registry is managed elsewhere (the portal,
# `fm registry enable`, or another Terraform state) and you only need to know
# where to push.
data "frostmoln_container_registry" "current" {}

# `enabled` is false rather than an error for a tenant that has not opted in, so
# check it before building an image reference.
output "image_prefix" {
  value = data.frostmoln_container_registry.current.enabled ? format(
    "%s/%s",
    data.frostmoln_container_registry.current.endpoint,
    data.frostmoln_container_registry.current.namespace,
  ) : null
}
