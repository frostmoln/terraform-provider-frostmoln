resource "frostmoln_postgres_read_replica" "reader" {
  instance_id = frostmoln_postgres_instance.main.id
  name        = "postgres-reader-1"

  # Optional — omit to inherit the primary's flavor.
  flavor_id = "db.gp1.small"
}

output "postgres_reader_endpoint" {
  value = "${frostmoln_postgres_read_replica.reader.private_ip}:${frostmoln_postgres_read_replica.reader.port}"
}
