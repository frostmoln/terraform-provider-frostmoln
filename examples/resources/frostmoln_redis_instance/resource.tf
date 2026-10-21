resource "frostmoln_redis_instance" "cache" {
  name      = "my-cache"
  version   = "7.2"
  flavor_id = "cache.mo1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "rdb"
  eviction_policy  = "allkeys-lru"

  # TLS: omit require_tls for the platform default (TLS only, on tls_port 6380,
  # with the plaintext port 6379 closed and `port` null, once TLS for managed
  # caches is available in the deployment). Set it to false only for clients
  # that cannot do TLS (6379 then stays open beside 6380). Fixed at create;
  # changing it replaces the instance.
  # require_tls = false

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

# With TLS: rediss:// on the hostname, verified against the bundle from the
# frostmoln_ca_bundle data source (redis-cli --tls --cacert <bundle> --sni <hostname>).
# port is null when the instance requires TLS.
output "redis_tls_endpoint" {
  value = frostmoln_redis_instance.cache.tls_enabled ? "rediss://${frostmoln_redis_instance.cache.hostname}:${frostmoln_redis_instance.cache.tls_port}" : null
}
