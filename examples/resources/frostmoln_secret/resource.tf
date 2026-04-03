resource "frostmoln_secret" "database_password" {
  name         = "prod/database/password"
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
