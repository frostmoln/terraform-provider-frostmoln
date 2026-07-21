data "frostmoln_valkey_instance" "cache" {
  id = "valkey-abc123"
}

output "valkey_private_ip" {
  value = data.frostmoln_valkey_instance.cache.private_ip
}

output "valkey_port" {
  value = data.frostmoln_valkey_instance.cache.port
}
