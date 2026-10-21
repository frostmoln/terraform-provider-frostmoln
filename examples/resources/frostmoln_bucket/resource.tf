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

# A bucket whose objects anyone can download by URL, without credentials:
# every object in it, including ones uploaded later. Listing the bucket still
# requires credentials. Unlike the AWS S3 canned ACL of the same name, this
# does not allow anonymous listing.
resource "frostmoln_bucket" "public_assets" {
  name = "my-public-assets"
  acl  = "public-read"
}
