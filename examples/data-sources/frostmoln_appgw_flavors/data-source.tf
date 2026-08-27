# The max_* values are structural caps, not guidance: creating more listeners,
# routes, backends or WAF rules than the size allows is refused. Size for the
# ruleset you intend to write, not only for the traffic.

data "frostmoln_appgw_flavors" "available" {}

locals {
  # The smallest size that can hold the ruleset we are about to write.
  suitable = [
    for f in data.frostmoln_appgw_flavors.available.flavors :
    f if f.max_waf_rules >= 200 && f.max_routes >= 50
  ]
}

output "smallest_suitable_flavor" {
  value = local.suitable[0].id
}
