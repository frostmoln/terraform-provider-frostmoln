resource "fm_volume_attachment" "data" {
  volume_id   = fm_volume.data.id
  instance_id = fm_instance.example.id
  device_path = "/dev/vdb"
}
