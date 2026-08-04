# Expire objects on a schedule.
#
# There is no storage-class transition rule and no per-object-tag filter: objects
# are not moved between storage classes on a schedule, and the lifecycle API does
# not model tag filters. Both are rejected outright rather than accepted and
# ignored, so scope a rule with prefix.
#
# A bucket carries a SINGLE lifecycle configuration and this resource replaces it
# whole.

resource "frostmoln_bucket_lifecycle_configuration" "example" {
  bucket = frostmoln_bucket.example.name

  rules = [
    {
      id              = "expire-old-logs"
      enabled         = true
      prefix          = "logs/"
      expiration_days = 30
    },
    {
      id                                     = "tidy-up"
      enabled                                = true
      noncurrent_version_expiration_days     = 90
      abort_incomplete_multipart_upload_days = 7
    },
  ]
}
