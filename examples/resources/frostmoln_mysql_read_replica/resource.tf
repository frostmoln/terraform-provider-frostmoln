resource "frostmoln_mysql_read_replica" "reader" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "mysql-reader-1"

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

output "mysql_reader_endpoint" {
  value = "${frostmoln_mysql_read_replica.reader.private_ip}:${frostmoln_mysql_read_replica.reader.port}"
}

# A replica has its own hostname and certificate (VERIFY_IDENTITY against it),
# and its primary's require_tls.
output "mysql_reader_hostname" {
  value = frostmoln_mysql_read_replica.reader.tls_enabled ? frostmoln_mysql_read_replica.reader.hostname : null
}
