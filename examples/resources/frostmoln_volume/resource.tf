resource "frostmoln_volume" "data" {
  name        = "app-data-volume"
  description = "Persistent data volume for application"
  size_gb     = 100
  volume_type = "ssd"
  zone        = "falkenberg"

  tags = {
    purpose = "data"
  }
}
