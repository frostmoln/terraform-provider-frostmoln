# These settings are AUTHORED, not enforced. What the gateway inspects with is
# the last PUBLISHED version — see frostmoln_appgw_waf_policy_publication.

resource "frostmoln_appgw_waf_policy" "main" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "default"

  # Start in detect: the ruleset runs and records what it WOULD have done, and
  # blocks nothing. Move to block once a dry-run has shown you what changes.
  mode           = "detect"
  paranoia_level = 2

  # What happens if the inspection engine is unavailable. An explicit choice,
  # never an accident.
  fail_mode = "open"

  request_body_limit_bytes = 10 * 1024 * 1024
}
