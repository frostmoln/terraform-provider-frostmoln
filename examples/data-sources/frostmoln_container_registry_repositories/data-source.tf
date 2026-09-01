# Every repository in this tenant's container registry namespace.
#
# The registry must already be enabled: a tenant that has not opted in gets an
# error here, not an empty list, so that "this tenant has no registry" and "this
# registry holds no images" stay distinguishable.
data "frostmoln_container_registry_repositories" "all" {}

# The endpoint and namespace a pull reference is built from.
data "frostmoln_container_registry" "current" {}

# Repository names are RELATIVE to the namespace and may themselves contain "/",
# so join them onto the namespace rather than assuming a single path segment.
output "repository_pull_references" {
  value = [
    for r in data.frostmoln_container_registry_repositories.all.repositories :
    format(
      "%s/%s/%s",
      data.frostmoln_container_registry.current.endpoint,
      data.frostmoln_container_registry.current.namespace,
      r.name,
    )
  ]
}

# Repositories nothing has ever pulled - a candidate list for a cleanup review.
#
# These counters are OBSERVATIONAL: they move on every push and pull, with no
# Terraform change involved, so read them and report on them; never write a
# configuration that plans a change from them.
output "never_pulled_repositories" {
  value = [
    for r in data.frostmoln_container_registry_repositories.all.repositories :
    r.name if r.pull_count == 0
  ]
}
