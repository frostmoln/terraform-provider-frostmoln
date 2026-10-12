resource "frostmoln_mysql_backup" "daily" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "daily-backup"
  type        = "full"

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}
