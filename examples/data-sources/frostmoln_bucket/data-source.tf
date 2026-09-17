# Resolve an object-storage bucket that Terraform did not create
# (provisioned in the portal or with `fm`). A bucket's name IS its id, so
# there is nothing to resolve but the name.
data "frostmoln_bucket" "billing_backups" {
  name = "billing-backups"
}

# The resolved bucket feeds references elsewhere — e.g. an S3 credential
# scoped to it, or a check block pinning its exposure.
check "billing_backups_stays_private" {
  assert {
    condition     = data.frostmoln_bucket.billing_backups.acl == "private"
    error_message = "the billing-backups bucket must stay private"
  }
}

# object_count and total_size (bytes) are measured answers: an empty bucket
# renders 0, not null. quota_bytes is null when the bucket is unlimited.
output "billing_backups_usage" {
  value = {
    objects = data.frostmoln_bucket.billing_backups.object_count
    bytes   = data.frostmoln_bucket.billing_backups.total_size
  }
}

# The CORS rules and lifecycle rules are deliberately NOT exported here: they
# belong to the dedicated configuration resources.
#   frostmoln_bucket_cors_configuration
#   frostmoln_bucket_lifecycle_configuration
