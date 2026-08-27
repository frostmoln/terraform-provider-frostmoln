# The rules YOU author. Platform-owned rules are excluded, so an emergency
# virtual patch the platform ships does not dirty your plan.

data "frostmoln_appgw_waf_rules" "mine" {
  gateway_id = frostmoln_application_gateway.edge.id
  policy_id  = frostmoln_appgw_waf_policy.main.id

  # "draft" (default) is what is being edited; "active" is what the gateway is
  # enforcing right now. They differ whenever there are unpublished changes.
  source = "active"
}

output "enforced_rule_count" {
  value = length(data.frostmoln_appgw_waf_rules.mine.rules)
}
