resource "frostmoln_mysql_backup" "daily" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "daily-backup"
  type        = "full"
}
