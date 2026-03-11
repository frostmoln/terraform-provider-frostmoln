resource "frostmoln_redis_instance" "cache" {
  name           = "my-cache"
  engine_version = "7.2"
  flavor_id      = "cache.small"
  vpc_id         = frostmoln_vpc.main.id
  subnet_id      = frostmoln_subnet.private.id

  persistence_mode = "rdb"
  eviction_policy  = "allkeys-lru"
}
