resource "frostmoln_postgres_instance" "main" {
  name       = "app-db"
  version    = "16"
  flavor_id  = "db.gp1.small"
  storage_gb = 50
  vpc_id     = frostmoln_vpc.main.id
  subnet_id  = frostmoln_subnet.db.id

  backup_enabled        = true
  backup_schedule       = "0 2 * * *"
  backup_retention_days = 35
}

output "postgres_endpoint" {
  value = "${frostmoln_postgres_instance.main.private_ip}:${frostmoln_postgres_instance.main.port}"
}
