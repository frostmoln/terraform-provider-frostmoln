resource "fm_bucket" "example" {
  name          = "my-data-bucket"
  region        = "sweden"
  storage_class = "standard"
  versioning    = "enabled"

  tags = {
    environment = "production"
    team        = "platform"
  }
}
