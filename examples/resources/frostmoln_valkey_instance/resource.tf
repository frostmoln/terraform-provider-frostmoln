resource "frostmoln_valkey_instance" "cache" {
  name      = "app-cache"
  version   = "8.1"
  flavor_id = "cache.gp1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "rdb"
  eviction_policy  = "allkeys-lru"
}

output "valkey_endpoint" {
  value = "${frostmoln_valkey_instance.cache.private_ip}:${frostmoln_valkey_instance.cache.port}"
}
