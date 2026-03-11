data "frostmoln_redis_instance" "cache" {
  id = "redis-abc123"
}

output "redis_private_ip" {
  value = data.frostmoln_redis_instance.cache.private_ip
}

output "redis_port" {
  value = data.frostmoln_redis_instance.cache.port
}
