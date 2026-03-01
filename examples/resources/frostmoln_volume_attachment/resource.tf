resource "frostmoln_volume_attachment" "data" {
  volume_id   = frostmoln_volume.data.id
  instance_id = frostmoln_instance.example.id
  device_path = "/dev/vdb"
}
