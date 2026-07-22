resource "frostmoln_redis_instance" "cache" {
  name      = "my-cache"
  version   = "7.2"
  flavor_id = "cache.gp1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "rdb"
  eviction_policy  = "allkeys-lru"

  # Scheduled backups require a persistence_mode other than "none".
  backup_enabled        = true
  backup_schedule       = "0 2 * * *"
  backup_retention_days = 35
}
