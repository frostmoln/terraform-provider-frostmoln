resource "fm_bucket" "example" {
  name          = "my-data-bucket"
  region        = "eu-north-1"
  storage_class = "standard"
  versioning    = "enabled"

  tags = {
    environment = "production"
    team        = "platform"
  }
}
