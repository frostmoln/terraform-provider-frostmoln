resource "frostmoln_volume" "data" {
  name        = "app-data-volume"
  description = "Persistent data volume for application"
  # Requested in GB (10^9 bytes). The volume is allocated the smallest whole
  # unit of 2^30 bytes that holds it, so size_bytes reads 100931731456
  # (100.93 GB) while size_gb stays 100.
  size_gb     = 100
  volume_type = "ssd"
  zone        = "falkenberg"

  tags = {
    purpose = "data"
  }
}

output "data_volume_size_bytes" {
  value = frostmoln_volume.data.size_bytes
}
