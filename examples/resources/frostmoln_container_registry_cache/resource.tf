resource "frostmoln_container_registry" "example" {}

# The upstream catalog is SERVER-OWNED. Read it rather than writing a literal
# list of keys: rows are added and retired without a provider release, so a key
# copied out of documentation can be one the server no longer offers.
data "frostmoln_container_registry_cache_upstreams" "available" {
  depends_on = [frostmoln_container_registry.example]
}

# A cache for an upstream that needs no credentials of your own.
resource "frostmoln_container_registry_cache" "ghcr" {
  upstream = "ghcr"

  # A cache cannot be created before the registry exists.
  depends_on = [frostmoln_container_registry.example]
}

# Docker Hub demands YOUR OWN credentials: an anonymous mirror of it draws on an
# allowance shared with other customers, so the server refuses one. Which
# upstreams demand them is the `requires_credentials` flag on the catalog row —
# read the flag, do not special-case the key.
#
# These are WRITE-ONLY. No endpoint returns them, so Terraform reports whatever
# it last wrote and cannot detect a change made elsewhere. There is no update
# route either: changing the password REPLACES the cache and deletes what it had
# cached.
resource "frostmoln_container_registry_cache" "dockerhub" {
  upstream = "dockerhub"
  username = var.dockerhub_username

  # password_wo is never written to the plan or to Terraform state (Terraform
  # 1.11+). Because Terraform cannot see it, it cannot detect a change to it:
  # password_wo_version is the ONLY signal that makes a later apply send the
  # current value — and it does so by REPLACING the cache, deleting what it had
  # cached. Editing the token without bumping the version does nothing.
  #
  # `password` is the older spelling, mutually exclusive with this one; it works
  # on any Terraform version but is stored in state in plaintext.
  password_wo         = var.dockerhub_token
  password_wo_version = "1"

  depends_on = [frostmoln_container_registry.example]
}

variable "dockerhub_username" {
  type = string
}

variable "dockerhub_token" {
  type      = string
  sensitive = true
}

# Driving caches from the catalog itself, so the configuration cannot name a key
# the server has retired. Only the upstreams needing no credentials are taken
# here; one that requires them needs its own resource with its own secrets.
resource "frostmoln_container_registry_cache" "anonymous" {
  for_each = {
    for u in data.frostmoln_container_registry_cache_upstreams.available.upstreams :
    u.key => u if !u.requires_credentials
  }

  upstream = each.key
}

# What to prefix an image with to pull it through the cache. Pull with the SAME
# credential that reaches your repository namespace — `docker login` is per
# host, so one credential spans both, and existing credentials are extended onto
# a new cache automatically.
output "cached_image_examples" {
  description = "How to pull through each cache."
  value = {
    for k, c in frostmoln_container_registry_cache.anonymous :
    k => "${c.pull_path}/<image>:<tag>"
  }
}

# CACHED BYTES COUNT AGAINST YOUR STORAGE ALLOWANCE: one quota, one meter,
# shared with your repository namespace. A cache fills on PULL, so it can
# consume the allowance with no push from you. The aggregate figure is on the
# registry itself — there is deliberately no per-cache number to add up.
output "registry_storage_used_bytes" {
  value = frostmoln_container_registry.example.storage_used_bytes
}
