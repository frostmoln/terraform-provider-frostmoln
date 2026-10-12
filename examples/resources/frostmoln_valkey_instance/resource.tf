resource "frostmoln_valkey_instance" "cache" {
  name      = "app-cache"
  version   = "8.1"
  flavor_id = "cache.mo1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "rdb"
  eviction_policy  = "allkeys-lru"

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

output "valkey_endpoint" {
  value = "${frostmoln_valkey_instance.cache.private_ip}:${frostmoln_valkey_instance.cache.port}"
}
