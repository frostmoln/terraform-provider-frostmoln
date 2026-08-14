data "frostmoln_api_key_scopes" "all" {}

# Discover what can be granted, and what each scope allows:
#   terraform console
#   > data.frostmoln_api_key_scopes.all.scopes
output "available_scopes" {
  value = { for s in data.frostmoln_api_key_scopes.all.scopes : s.scope => s.description }
}

# Grant explicitly. Do NOT derive a key's scopes from the catalog with a filter
# such as `if endswith(s.scope, ":read")`: the catalog is server-owned, so a
# scope added upstream would silently widen this key on the next apply.
resource "frostmoln_api_key" "ci" {
  name   = "ci-deploy"
  scopes = ["compute:read", "compute:write"]
}
