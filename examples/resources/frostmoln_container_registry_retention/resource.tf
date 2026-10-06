# The tenant's single tag-retention policy. It runs nightly, between 00:00
# and 06:00 UTC, and counts images per repository. Pull-through caches are
# never touched.
#
# Applying replaces the whole policy. A rule left unset keeps everything in its
# class: here, untagged images older than 14 days are deleted and every
# repository keeps its 20 most recently pushed tagged images.
resource "frostmoln_container_registry" "example" {}

resource "frostmoln_container_registry_retention" "example" {
  keep_last_tagged           = 20
  delete_untagged_after_days = 14

  depends_on = [frostmoln_container_registry.example]
}
