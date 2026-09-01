# Every artifact in one repository. `repository` is RELATIVE to the tenant's
# namespace - the `name` of a frostmoln_container_registry_repositories entry,
# never a full image reference - and it may contain "/".
data "frostmoln_container_registry_artifacts" "api_server" {
  repository = "team/api-server"
}

# Untagged, orphaned manifests: an EMPTY tag list means a later push moved the
# tag this artifact used to carry onto new content. It can no longer be pulled by
# name, only by digest, and it still occupies the namespace's storage. Listing
# them is the point, not an error.
output "orphaned_digests" {
  value = [
    for a in data.frostmoln_container_registry_artifacts.api_server.artifacts :
    a.digest if length(a.tags) == 0
  ]
}

# pulled_at is NULL - never a zero timestamp, never "unknown" - for an artifact
# nothing has ever fetched, so a null test is a sound one.
output "never_pulled_digests" {
  value = [
    for a in data.frostmoln_container_registry_artifacts.api_server.artifacts :
    a.digest if a.pulled_at == null
  ]
}

# DO NOT sum size_bytes to work out storage usage. A layer shared between
# artifacts is stored once and counted once against the cap, but counted in full
# in every artifact that references it, so a sum overstates the namespace - and
# deleting an artifact frees only the layers nothing else references. The
# registry's own figure is the only correct one.
data "frostmoln_container_registry" "current" {}

output "namespace_storage_used_bytes" {
  value = data.frostmoln_container_registry.current.storage_used_bytes
}

# size_bytes answers "how big is this image", which is a fair question per
# artifact - it is only the addition that is wrong.
output "artifact_sizes_by_digest" {
  value = {
    for a in data.frostmoln_container_registry_artifacts.api_server.artifacts :
    a.digest => a.size_bytes
  }
}

# WHAT TO DO WITH THE ANSWER. There is no artifact RESOURCE and there will not
# be one: Terraform never pushed these images, so wrapping them in a resource
# would give it a Delete with no Create — and turn any `terraform destroy` into
# the silent removal of content a pipeline produced. Use the list to decide, and
# delete deliberately, with a tool that knows it is destroying something:
#
#   fm registry image delete web/api sha256:<digest>
#
# Every untagged manifest in the repository, ready to pipe:
output "untagged_digests" {
  value = [for a in data.frostmoln_container_registry_artifacts.api_server.artifacts : a.digest if length(a.tags) == 0]
}
