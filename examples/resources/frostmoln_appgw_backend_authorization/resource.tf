# Opens the path from the gateway's appliance to your backends.
#
# The rule created is per (security group, protocol, port) — NOT per backend.
# Declare this ONCE per (security group, port), not once per backend: two
# backends behind the same group and port share one rule, and destroying either
# resource would close the path for both.

resource "frostmoln_appgw_backend_authorization" "web" {
  gateway_id        = frostmoln_application_gateway.edge.id
  pool_id           = frostmoln_appgw_backend_pool.web.id
  backend_id        = frostmoln_appgw_backend.web_1.id
  security_group_id = frostmoln_security_group.web.id
}
