resource "frostmoln_mysql_read_replica" "reader" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "mysql-reader-1"
}

output "mysql_reader_endpoint" {
  value = "${frostmoln_mysql_read_replica.reader.private_ip}:${frostmoln_mysql_read_replica.reader.port}"
}
