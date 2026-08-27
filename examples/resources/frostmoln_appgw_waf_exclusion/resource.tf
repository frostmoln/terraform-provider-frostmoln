# Prefer an exclusion over disabling a rule: it narrows the rule to stop
# matching one known-good field on one path, rather than turning it off
# everywhere.

resource "frostmoln_appgw_waf_exclusion" "search_query" {
  gateway_id = frostmoln_application_gateway.edge.id
  policy_id  = frostmoln_appgw_waf_policy.main.id
  rule_key   = "allow-sql-in-search-q"

  target_secrule_id = 942100

  variable     = "ARGS"
  selector     = "q"
  path_pattern = "^/search"

  description = "The search box accepts free text that trips the SQLi rules."
}
