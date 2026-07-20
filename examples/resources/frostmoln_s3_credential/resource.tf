resource "frostmoln_s3_credential" "example" {
  name        = "app-s3-access"
  description = "S3 credentials for application backend"

  # Scope the credential — omitting these grants ALL buckets and ALL actions.
  # Every scope attribute is ForceNew: changing one replaces the credential and
  # issues a new secret_access_key.
  allowed_buckets = [frostmoln_bucket.example.name]
  allowed_actions = ["s3:GetObject", "s3:PutObject", "s3:ListBucket"]
}

output "s3_secret_key" {
  value     = frostmoln_s3_credential.example.secret_access_key
  sensitive = true
}
