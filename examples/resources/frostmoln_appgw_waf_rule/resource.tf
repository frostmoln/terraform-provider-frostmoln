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
        operator        = "@rx"
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
        operator = "@beginsWith"
        value    = "/admin"
      },
      {
        variable = "REMOTE_ADDR"
        operator = "@ipMatch"
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

# Exempt traffic you know is safe from the anomaly-score decision — the one
# that refuses a request for accumulating enough suspicion, and the one that
# produces false positives. An uptime checker, a webhook sender, or a payload
# shape that looks like an attack and is not.
#
# THREE THINGS TO KNOW BEFORE YOU WRITE ONE.
#
#   EVERY CONDITION MUST MATCH. This rule exempts /health ONLY when the request
#   also carries the token — the two conditions are ANDed, exactly as they are
#   in a rule that denies. It is not "/health, for anyone".
#
#   IT EXEMPTS LESS THAN THE WORD SUGGESTS. The policy's allowed_methods and
#   allowed_request_content_types, a request body the gateway could not parse,
#   your own denying rules and any rule Frostmoln placed on the policy all
#   still refuse, because this rule runs after them. An allowed request is also
#   not scored, so it carries no anomaly-score line in the inspection record.
#
#   phase = 2 IS REQUIRED, and phase 1 is refused rather than moved: there the
#   rule would also skip the gateway's own method and content-type refusals,
#   which are not exemptable. A rule that only reads headers still works at
#   phase 2 — the headers are there.
#
# `status` is the response code for a deny and is ignored here, so it is left
# out. `message` is recorded whatever the action is, and is how you tell which
# of your rules exempted a request.
resource "frostmoln_appgw_waf_rule" "allow_uptime_checker" {
  gateway_id  = frostmoln_application_gateway.edge.id
  policy_id   = frostmoln_appgw_waf_policy.main.id
  rule_key    = "allow-uptime-checker"
  kind        = "builder"
  ordinal     = 20
  description = "Our uptime checker posts a payload the managed rules keep scoring."

  builder_json = jsonencode({
    phase = 2
    conditions = [
      {
        variable = "REQUEST_URI"
        operator = "@beginsWith"
        value    = "/health"
      },
      {
        variable = "REQUEST_HEADERS"
        selector = "X-Uptime-Token"
        operator = "@streq"
        value    = "replace-me"
      }
    ]
    action = {
      type    = "allow"
      message = "uptime checker"
    }
  })
}

# Record a match and change nothing else — how you measure what a rule WOULD
# refuse before you make it refuse anything.
resource "frostmoln_appgw_waf_rule" "watch_legacy_client" {
  gateway_id  = frostmoln_application_gateway.edge.id
  policy_id   = frostmoln_appgw_waf_policy.main.id
  rule_key    = "watch-legacy-client"
  kind        = "builder"
  ordinal     = 30
  description = "Count requests from the client we are about to retire."

  builder_json = jsonencode({
    phase = 1
    conditions = [
      {
        variable = "REQUEST_HEADERS"
        selector = "User-Agent"
        operator = "@beginsWith"
        value    = "acme-legacy/"
      }
    ]
    action = {
      type    = "log"
      message = "legacy client"
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
