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

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

output "mysql_endpoint" {
  value = "${frostmoln_mysql_instance.main.private_ip}:${frostmoln_mysql_instance.main.port}"
}
