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

  # The methods this gateway accepts. An OVERRIDE: omit it and you run on the
  # platform default, which is kept current for you.
  #
  # It is a deny-list of four, not a ceiling of seven. TRACE, TRACK, CONNECT and
  # DEBUG are refused on every gateway and may never be listed; everything else
  # is yours to name -- including the WebDAV and CalDAV verbs, which the default
  # does NOT carry and which are therefore refused until you list them. That is
  # the usual reason to set this attribute at all.
  allowed_methods = ["GET", "HEAD", "POST", "PUT", "DELETE", "PROPFIND", "REPORT"]

  # REMOVING EITHER ATTRIBUTE FROM THIS BLOCK CLEARS THE OVERRIDE and returns
  # the policy to the platform default. The plan shows it as a change and the
  # apply performs it. An empty list is refused rather than accepted as a way to
  # say the same thing -- it is not a narrowing to nothing, and it could not be
  # kept in state.
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

# A policy that accepts a vendor media type its API actually serves.
#
# The content-type list may ADD types as well as remove them, and the
# alternative to adding one is turning the whole content-type check off. Write
# each entry lowercase as type/subtype with no parameters: the ruleset matches
# the type alone, so "application/json; charset=utf-8" would never match.
resource "frostmoln_appgw_waf_policy" "vendor_api" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "vendor-api"

  allowed_request_content_types = [
    "application/json",
    "application/vnd.acme.v2+json",
    "multipart/form-data",
  ]
}

# WHAT WILL ACTUALLY COMPILE: the override where one is set, the platform
# default otherwise. Render this rather than keeping a copy of the default.
#
# Never feed it back into allowed_request_content_types. The platform default
# has been widened three times, and a configuration that copies today's list
# into the override pins you out of the next widening -- with a plan that looks
# like it changes nothing.
#
# Null on a listener- or route-scoped policy, which does not decide it.
output "gateway_effective_methods" {
  value = frostmoln_appgw_waf_policy.main.effective_allowed_methods
}

output "vendor_api_effective_content_types" {
  value = frostmoln_appgw_waf_policy.vendor_api.effective_allowed_request_content_types
}
