resource "frostmoln_mysql_instance" "main" {
  name       = "my-mysql-db"
  version    = "8.4"
  flavor     = "db.small"
  storage_gb = 50
  vpc_id     = frostmoln_vpc.main.id
  subnet_id  = frostmoln_subnet.db.id

  ha_enabled            = true
  backup_enabled        = true
  backup_schedule       = "0 2 * * *"
  backup_retention_days = 35
}

output "mysql_endpoint" {
  value = "${frostmoln_mysql_instance.main.private_ip}:${frostmoln_mysql_instance.main.port}"
}
