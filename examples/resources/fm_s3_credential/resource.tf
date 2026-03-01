resource "fm_s3_credential" "example" {
  name        = "app-s3-access"
  description = "S3 credentials for application backend"
}

output "s3_access_key" {
  value = fm_s3_credential.example.id
}

output "s3_secret_key" {
  value     = fm_s3_credential.example.secret_access_key
  sensitive = true
}
