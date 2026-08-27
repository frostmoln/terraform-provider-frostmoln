# Rules land on the policy's DRAFT and change nothing a request sees until a
# frostmoln_appgw_waf_policy_publication publishes them.

# The structured surface. It is lowered to the engine's own rule language by the
# same renderer everything else goes through, so a dry-run cannot disagree with
# what is enforced.
resource "frostmoln_appgw_waf_rule" "block_scanners" {
  gateway_id  = frostmoln_application_gateway.edge.id
  policy_id   = frostmoln_appgw_waf_policy.main.id
  rule_key    = "block-known-scanners"
  kind        = "builder"
  description = "Refuse traffic identifying itself as a vulnerability scanner."

  builder_json = jsonencode({
    phase = 1
    conditions = [
      {
        variable        = "REQUEST_HEADERS"
        selector        = "User-Agent"
        operator        = "rx"
        value           = "(?i)sqlmap|nikto|nessus"
        transformations = ["lowercase"]
      }
    ]
    action = {
      type    = "deny"
      status  = 403
      message = "known scanner"
    }
  })
}

# Restrict an endpoint by source address.
resource "frostmoln_appgw_waf_rule" "admin_office_only" {
  gateway_id  = frostmoln_application_gateway.edge.id
  policy_id   = frostmoln_appgw_waf_policy.main.id
  rule_key    = "admin-office-only"
  kind        = "builder"
  ordinal     = 10
  description = "/admin is reachable from the office range only."

  builder_json = jsonencode({
    phase = 1
    conditions = [
      {
        variable = "REQUEST_URI"
        operator = "beginsWith"
        value    = "/admin"
      },
      {
        variable = "REMOTE_ADDR"
        operator = "ipMatch"
        value    = "198.51.100.0/24"
        negated  = true
      }
    ]
    action = {
      type    = "deny"
      status  = 403
      message = "admin is office-only"
    }
  })
}

# Turn one managed rule off where it is a false positive for your traffic.
# Prefer a frostmoln_appgw_waf_exclusion where you can: it narrows the rule to
# one field on one path instead of disabling it everywhere.
resource "frostmoln_appgw_waf_rule" "quiet_942100" {
  gateway_id         = frostmoln_application_gateway.edge.id
  policy_id          = frostmoln_appgw_waf_policy.main.id
  rule_key           = "quiet-942100"
  kind               = "managedOverride"
  managed_secrule_id = 942100
  managed_action     = "disable"
  description        = "Our search endpoint legitimately posts SQL-shaped text."
}

# NOTE: kind = "raw" is not usable from Terraform today. The platform requires
# the rule text to carry the rule id it allocates during the same write, and a
# configuration cannot reference its own computed secrule_id. Use "builder" or
# "managedOverride".
