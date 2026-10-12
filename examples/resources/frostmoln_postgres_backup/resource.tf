resource "frostmoln_postgres_backup" "pre_upgrade" {
  instance_id = frostmoln_postgres_instance.main.id
  name        = "pre-upgrade"
  type        = "full"

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

# Only "full" backups are yours to create. The platform also takes `base`
# backups of its own on instances with point-in-time recovery; they appear in
# the backup list, cannot be deleted, and are not managed here — a
# point-in-time restore uses `restore_from` on frostmoln_postgres_instance.
