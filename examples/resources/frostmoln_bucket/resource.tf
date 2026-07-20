resource "frostmoln_bucket" "example" {
  name          = "my-data-bucket"
  region        = "sweden"
  storage_class = "STANDARD"
  versioning    = "enabled"

  tags = {
    environment = "production"
    team        = "platform"
  }
}
