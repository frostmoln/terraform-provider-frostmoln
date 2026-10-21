data "frostmoln_redis_instance" "cache" {
  id = "redis-abc123"
}

output "redis_private_ip" {
  value = data.frostmoln_redis_instance.cache.private_ip
}

# Null when the instance requires TLS (the plaintext port is closed).
output "redis_port" {
  value = data.frostmoln_redis_instance.cache.port
}

output "redis_tls" {
  value = {
    required = data.frostmoln_redis_instance.cache.require_tls
    enabled  = data.frostmoln_redis_instance.cache.tls_enabled
    hostname = data.frostmoln_redis_instance.cache.hostname
    port     = data.frostmoln_redis_instance.cache.tls_port
  }
}
