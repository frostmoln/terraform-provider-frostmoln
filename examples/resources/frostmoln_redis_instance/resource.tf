resource "frostmoln_redis_instance" "cache" {
  name      = "my-cache"
  version   = "7.2"
  flavor_id = "cache.mo1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "rdb"
  eviction_policy  = "allkeys-lru"

  # Scheduled backups require a persistence_mode other than "none".
  backup_enabled        = true
  backup_schedule       = "0 2 * * *"
  backup_retention_days = 35

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}
