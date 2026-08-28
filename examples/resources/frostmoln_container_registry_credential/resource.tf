resource "frostmoln_container_registry" "example" {}

# The secret is returned ONCE, at creation, and there is no rotation endpoint.
# Terraform keeps it in state — so keep your state encrypted and access
# controlled. A secret lost outside Terraform is recovered by replacing the
# credential (`terraform taint`, or changing name/capability), which mints a new
# one and revokes the old.
resource "frostmoln_container_registry_credential" "ci" {
  name       = "ci-pipeline"
  capability = "push" # "pull" (the default) or "push"; push implies pull

  # The credential cannot be minted before the registry exists.
  depends_on = [frostmoln_container_registry.example]
}

output "docker_login" {
  # The username is single-quoted on purpose: it carries a `robot$` prefix, and
  # unquoted in bash or zsh the `$t...` is expanded away, so the login is refused
  # and the credential looks broken.
  description = "How to authenticate to the registry."
  value = format(
    "docker login %s -u '%s'",
    frostmoln_container_registry_credential.ci.endpoint,
    frostmoln_container_registry_credential.ci.username,
  )
}

output "registry_password" {
  value     = frostmoln_container_registry_credential.ci.secret
  sensitive = true
}
