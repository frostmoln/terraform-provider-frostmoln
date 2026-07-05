resource "fm_api_key" "ci" {
  name        = "ci-deploy-key"
  description = "API key for CI/CD pipeline deployments"
  scopes      = ["compute:write", "network:read", "storage:read"]
  expires_at  = "2027-01-01" # bare date = end of that day (UTC); RFC3339 also accepted
  rate_limit  = 5000
}

output "api_key_prefix" {
  value = fm_api_key.ci.key_prefix
}
