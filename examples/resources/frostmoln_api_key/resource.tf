# An API key. Flat scopes are RETIRED: the API refuses every non-empty `scopes`
# value with 400 API_KEY_SCOPES_RETIRED, so the attribute is removed and the
# key's continuing authority is an access policy attached with
# frostmoln_iam_policy_attachment.
resource "frostmoln_api_key" "ci" {
  name        = "ci-deploy-key"
  description = "API key for CI/CD pipeline deployments"
  # expires_at is optional. A bare date means end of that day (UTC); RFC3339 is
  # also accepted. A date already in the past is accepted and yields an
  # immediately expired key, so set one that is still ahead of today:
  #   expires_at = "2030-01-01"
  rate_limit = 5000
}

output "api_key_prefix" {
  value = frostmoln_api_key.ci.key_prefix
}
