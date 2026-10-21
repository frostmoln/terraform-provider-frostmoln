resource "frostmoln_postgres_read_replica" "reader" {
  instance_id = frostmoln_postgres_instance.main.id
  name        = "postgres-reader-1"

  # Optional — omit to inherit the primary's flavor.
  flavor_id = "db.gp1.small"

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

output "postgres_reader_endpoint" {
  value = "${frostmoln_postgres_read_replica.reader.private_ip}:${frostmoln_postgres_read_replica.reader.port}"
}

# A replica has its own hostname and certificate (verify-full against it), and
# its primary's require_tls.
output "postgres_reader_hostname" {
  value = frostmoln_postgres_read_replica.reader.tls_enabled ? frostmoln_postgres_read_replica.reader.hostname : null
}
