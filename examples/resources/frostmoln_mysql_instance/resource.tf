resource "frostmoln_mysql_instance" "main" {
  name       = "my-mysql-db"
  version    = "8.4"
  flavor_id  = "db.gp1.small"
  storage_gb = 50
  vpc_id     = frostmoln_vpc.main.id
  subnet_id  = frostmoln_subnet.db.id

  backup_enabled        = true
  backup_schedule       = "0 2 * * *"
  backup_retention_days = 35

  # TLS: omit require_tls for the platform default (TLS required, plaintext
  # refused, once TLS for managed MySQL is available in the deployment).
  # Fixed at create; set it to false only for clients that cannot do TLS.
  # Changing it replaces the instance.
  # require_tls = false

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

output "mysql_endpoint" {
  value = "${frostmoln_mysql_instance.main.private_ip}:${frostmoln_mysql_instance.main.port}"
}

# With TLS the server's certificate is issued for its hostname by the
# Frostmoln customer CA: connect with ssl-mode=VERIFY_IDENTITY against the
# bundle from the frostmoln_ca_bundle data source (from inside the VPC, where
# the hostname resolves). hostname is null, with tls_enabled false, on an
# instance built before TLS.
output "mysql_verify_identity_endpoint" {
  value = frostmoln_mysql_instance.main.tls_enabled ? "${frostmoln_mysql_instance.main.hostname}:${frostmoln_mysql_instance.main.port}" : null
}
