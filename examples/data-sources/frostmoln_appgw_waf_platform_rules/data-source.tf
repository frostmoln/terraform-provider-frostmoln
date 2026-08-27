# The platform's own rules: gateway self-protection, and any emergency virtual
# patch in force. Read-only.
#
# Worth asserting on: a rule appearing here that you did not expect is the
# platform having shipped a patch, which is exactly the thing to notice.

data "frostmoln_appgw_waf_platform_rules" "in_force" {
  gateway_id = frostmoln_application_gateway.edge.id
  policy_id  = frostmoln_appgw_waf_policy.main.id
}

output "platform_patches_i_have_opted_out_of" {
  value = [
    for r in data.frostmoln_appgw_waf_platform_rules.in_force.rules :
    r.rule_key if r.opted_out
  ]
}
