data "frostmoln_apache_instance" "existing" {
  id = "web-abc123"
}

output "apache_existing_private_ip" {
  value = data.frostmoln_apache_instance.existing.private_ip
}

output "apache_existing_public_ip" {
  value = data.frostmoln_apache_instance.existing.public_ip
}

output "apache_existing_tls" {
  value = {
    enabled  = data.frostmoln_apache_instance.existing.tls_enabled
    required = data.frostmoln_apache_instance.existing.require_tls
    hostname = data.frostmoln_apache_instance.existing.hostname
  }
}
