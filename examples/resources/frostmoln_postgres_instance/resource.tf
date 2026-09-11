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

  # Customer-tunable wait budgets. Defaults: create/update/delete = 30m.
  # Raise for slow provisions (e.g. multi-VM HA pairs) without waiting on a
  # provider release; a timeouts change is an in-place no-op on live data.
  timeouts {
    create = "30m"
    update = "30m"
    delete = "30m"
  }
}

output "postgres_endpoint" {
  value = "${frostmoln_postgres_instance.main.private_ip}:${frostmoln_postgres_instance.main.port}"
}
