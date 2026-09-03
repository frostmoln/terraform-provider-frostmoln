# These settings are AUTHORED, not enforced. What the gateway inspects with is
# the last PUBLISHED version — see frostmoln_appgw_waf_policy_publication.

# The GATEWAY policy: it carries the managed ruleset for everything the gateway
# serves, and owns the dials that tune it.
resource "frostmoln_appgw_waf_policy" "main" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "default"
  scope      = "gateway" # the default

  # Start in detect: the ruleset runs and records what it WOULD have done, and
  # blocks nothing. Move to block once a dry-run has shown you what changes.
  mode           = "detect"
  paranoia_level = 2

  # What happens if the inspection engine is unavailable. An explicit choice,
  # never an accident.
  fail_mode = "open"

  # Between 4096 and 40960 bytes. The ceiling is the inspection engine's frame
  # size, not a memory budget, so it does not rise with a larger flavor.
  request_body_limit_bytes = 32768
}

# An OVERLAY policy: your own rules on one listener or one route. It is compiled
# WITHOUT the managed ruleset, so paranoia_level, anomaly_score_threshold and
# managed_ruleset_version do not belong here.
resource "frostmoln_appgw_waf_policy" "api" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "api-rules"
  scope      = "overlay"

  # "inherit" is the overlay default: it takes the GATEWAY policy's mode, so it
  # blocks when that policy blocks. It is not the cautious setting — read
  # effective_mode, never mode, to see what is actually in force.
  mode = "inherit"
}

# What THIS POLICY resolves to, with "inherit" resolved. A check written against
# `mode` would report an inheriting overlay as not blocking while its users'
# requests are being refused.
#
# Not "what is in force for traffic": the platform composes one ruleset per
# gateway, with the managed ruleset applied across the whole gateway and each
# scope's own rules added to it. An overlay adds rules to its listener or route;
# it never exempts that route from the gateway's baseline. So a detecting
# overlay does not mean traffic there is uninspected.
#
# It is null when it cannot be determined, so read it through try().
output "api_overlay_effective_mode" {
  value = try(frostmoln_appgw_waf_policy.api.effective_mode, "unknown")
}
