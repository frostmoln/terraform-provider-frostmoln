# The max_* values are structural caps, not guidance: creating more listeners,
# routes, backends, WAF rules or WAF exclusions than the size allows is refused.
# Size for the ruleset you intend to write, not only for the traffic.
#
# max_waf_rules and max_waf_exclusions are SEPARATE budgets, each counted across
# all of a gateway's WAF policies, so tuning away a false positive never costs
# you room for a rule. Exclusions are what routine tuning spends, and a gateway
# whose rule count looks comfortable can still run out of them.

data "frostmoln_appgw_flavors" "available" {}

locals {
  # The smallest size that can hold the ruleset we are about to write.
  suitable = [
    for f in data.frostmoln_appgw_flavors.available.flavors :
    f if f.max_waf_rules >= 200 && f.max_waf_exclusions >= 100 && f.max_routes >= 50
  ]
}

output "smallest_suitable_flavor" {
  value = local.suitable[0].id
}
