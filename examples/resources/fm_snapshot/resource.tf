resource "fm_snapshot" "backup" {
  name        = "data-volume-backup"
  description = "Daily backup of data volume"
  volume_id   = fm_volume.data.id

  tags = {
    type = "backup"
  }
}
