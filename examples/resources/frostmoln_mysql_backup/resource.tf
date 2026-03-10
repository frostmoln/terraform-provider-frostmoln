resource "frostmoln_mysql_backup" "daily" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "daily-backup"
  type        = "full"
}

resource "frostmoln_mysql_backup" "binlog" {
  instance_id = frostmoln_mysql_instance.main.id
  name        = "binlog-backup"
  type        = "binlog"
}
