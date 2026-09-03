# WHERE a policy applies is a separate act from WHAT it says. A policy exists,
# is authored and is published without being attached to anything, and
# destroying an attachment detaches the policy rather than deleting it.

# The gateway level takes a gateway-scoped policy — the one carrying the managed
# ruleset for everything this gateway serves.
resource "frostmoln_appgw_waf_policy_attachment" "gateway" {
  gateway_id = frostmoln_application_gateway.edge.id
  policy_id  = frostmoln_appgw_waf_policy.main.id
}

# A listener takes an overlay-scoped policy.
resource "frostmoln_appgw_waf_policy_attachment" "api_listener" {
  gateway_id  = frostmoln_application_gateway.edge.id
  listener_id = frostmoln_appgw_listener.https.id
  policy_id   = frostmoln_appgw_waf_policy.api.id
}

# So does a single route. A route id is unique only within its listener, so
# listener_id is required alongside it.
resource "frostmoln_appgw_waf_policy_attachment" "admin_route" {
  gateway_id  = frostmoln_application_gateway.edge.id
  listener_id = frostmoln_appgw_listener.https.id
  route_id    = frostmoln_appgw_route.admin.id
  policy_id   = frostmoln_appgw_waf_policy.admin.id
}

# Destroying the GATEWAY attachment changes every inheriting overlay: with no
# gateway policy to resolve against, an overlay whose mode is "inherit" falls
# back to detect and stops refusing anything.

# What the ATTACHED POLICY resolves to, with "inherit" resolved.
#
# Not "what is in force" for traffic through this listener. The platform builds
# ONE ruleset per gateway: the managed ruleset applies across the whole gateway,
# and each scope's customer rules are added inside it. An overlay adds rules to
# its listener or route and never exempts it from that baseline — so this value
# describes the overlay, not the protection the listener has.
#
# It is null when it cannot be determined, so read it through try().
output "api_listener_overlay_effective_mode" {
  value = try(frostmoln_appgw_waf_policy_attachment.api_listener.effective_mode, "unknown")
}
