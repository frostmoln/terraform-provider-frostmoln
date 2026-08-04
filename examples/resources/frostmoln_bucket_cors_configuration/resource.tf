# Let a web application call the bucket's S3 endpoint directly from the browser.
#
# A bucket carries a SINGLE CORS configuration and this resource replaces it
# whole. Frostmoln adds a default rule for the customer portal's origin to every
# new bucket, which is what lets the portal's object browser upload and list
# objects browser-direct — and that default is only ever applied at bucket
# create, so nothing puts it back.
#
# Read the bucket's current configuration before taking it over (terraform
# import, or the API) and re-declare the rules you want to keep. The portal's
# origin is deployment configuration, so do not assume a hostname: the plan
# names the origins it is about to stop allowing.

resource "frostmoln_bucket_cors_configuration" "example" {
  bucket = frostmoln_bucket.example.name

  rules = [
    {
      id              = "web-app"
      allowed_origins = ["https://app.example.com"]
      # Grant only the methods the application actually issues.
      allowed_methods = ["GET", "HEAD", "PUT"]
      expose_headers  = ["ETag"]
      max_age_seconds = 3600
    },
  ]
}
