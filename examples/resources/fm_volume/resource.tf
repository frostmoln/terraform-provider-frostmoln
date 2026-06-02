resource "fm_volume" "data" {
  name        = "app-data-volume"
  description = "Persistent data volume for application"
  size_gb     = 100
  volume_type = "ssd"
  zone        = "sweden-a"
  encrypted   = true

  tags = {
    purpose = "data"
  }
}
