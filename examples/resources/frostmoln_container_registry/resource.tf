# The explicit opt-in that creates this tenant's registry. Its only argument is
# `tags`: the namespace is derived from the tenant id and nothing else about a
# registry is configurable.
#
# Applying this against a tenant that already opted in (through the portal or
# `fm registry enable`) adopts the existing registry rather than failing.
#
# NOTE: `terraform destroy` does NOT delete the registry. There is no teardown
# endpoint — the resource is removed from state with a warning, and the
# namespace, its images and its billable state all survive.
resource "frostmoln_container_registry" "example" {
  # Resource tags: your own metadata for search and filtering — not image tags.
  # Merged with the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

output "registry_endpoint" {
  description = "The host to `docker login` to."
  value       = frostmoln_container_registry.example.endpoint
}

output "registry_image_prefix" {
  description = "Prefix every image reference with this."
  value       = "${frostmoln_container_registry.example.endpoint}/${frostmoln_container_registry.example.namespace}"
}
