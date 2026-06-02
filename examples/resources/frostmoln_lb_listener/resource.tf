# An HTTPS listener on a load balancer.
#
# allowed_cidrs is deny-by-default and required: at least one CIDR must be set.
# To accept connections from anywhere you must opt in explicitly with
# ["0.0.0.0/0"].
#
# Note: the listener<->pool link is declared on the POOL via its listener_id
# attribute (see frostmoln_lb_pool), NOT here via default_pool_id. Setting both
# directions would create an HCL dependency cycle (listener -> pool -> listener).
resource "frostmoln_lb_listener" "https" {
  load_balancer_id = frostmoln_load_balancer.web.id
  name             = "https"
  protocol         = "https"
  protocol_port    = 443

  allowed_cidrs = ["0.0.0.0/0"]

  insert_headers = {
    "X-Forwarded-For" = "true"
  }
}
