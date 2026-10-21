data "frostmoln_valkey_instance" "cache" {
  id = "valkey-abc123"
}

output "valkey_private_ip" {
  value = data.frostmoln_valkey_instance.cache.private_ip
}

# Null when the instance requires TLS (the plaintext port is closed).
output "valkey_port" {
  value = data.frostmoln_valkey_instance.cache.port
}

output "valkey_tls" {
  value = {
    required = data.frostmoln_valkey_instance.cache.require_tls
    enabled  = data.frostmoln_valkey_instance.cache.tls_enabled
    hostname = data.frostmoln_valkey_instance.cache.hostname
    port     = data.frostmoln_valkey_instance.cache.tls_port
  }
}
