# A backend pool for a load balancer.
#
# The pool's listener_id attaches this pool to a listener. This is the canonical
# direction for the listener<->pool link; do NOT also set default_pool_id on the
# listener, or Terraform will report a dependency cycle.
resource "frostmoln_lb_pool" "backend" {
  load_balancer_id = frostmoln_load_balancer.web.id
  listener_id      = frostmoln_lb_listener.https.id
  name             = "backend"
  protocol         = "http"
  lb_algorithm     = "round_robin"
  proxy_protocol   = "none"

  # Optional session persistence (nested attribute). Omit for none.
  session_persistence = {
    type                = "APP_COOKIE"
    cookie_name         = "SESSIONID"
    persistence_timeout = 3600
  }
}
