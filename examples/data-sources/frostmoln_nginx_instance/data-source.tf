data "frostmoln_nginx_instance" "existing" {
  id = "web-abc123"
}

output "nginx_existing_private_ip" {
  value = data.frostmoln_nginx_instance.existing.private_ip
}

output "nginx_existing_public_ip" {
  value = data.frostmoln_nginx_instance.existing.public_ip
}

output "nginx_existing_tls" {
  value = {
    enabled  = data.frostmoln_nginx_instance.existing.tls_enabled
    required = data.frostmoln_nginx_instance.existing.require_tls
    hostname = data.frostmoln_nginx_instance.existing.hostname
  }
}
