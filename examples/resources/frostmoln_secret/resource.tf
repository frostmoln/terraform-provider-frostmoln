resource "frostmoln_secret" "database_password" {
  name         = "prod-database-password"
  description  = "Production database master password"
  secret_value = var.db_password
  content_type = "text/plain"

  max_versions         = 5
  recovery_window_days = 14

  tags = {
    environment = "production"
    service     = "database"
  }
}

# Prefer the write-only form: secret_value_wo never reaches the plan or the
# state file. It needs Terraform 1.11 or later, and because Terraform cannot see
# a write-only value change, bump secret_value_wo_version to push a new one.
resource "frostmoln_secret" "api_token" {
  name        = "prod-payments-api-token"
  description = "Payment provider API token"

  secret_value_wo         = var.payments_api_token
  secret_value_wo_version = "1"
}
