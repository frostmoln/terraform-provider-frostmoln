resource "frostmoln_snapshot" "backup" {
  name        = "data-volume-backup"
  description = "Daily backup of data volume"
  volume_id   = frostmoln_volume.data.id

  # Tags change in place; a tag change (or a provider default_tags change) never
  # re-takes the snapshot.
  tags = {
    type = "backup"
  }
}
