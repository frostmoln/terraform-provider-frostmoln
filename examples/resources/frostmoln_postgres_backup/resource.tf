resource "frostmoln_postgres_backup" "pre_upgrade" {
  instance_id = frostmoln_postgres_instance.main.id
  name        = "pre-upgrade"
  type        = "full"
}
