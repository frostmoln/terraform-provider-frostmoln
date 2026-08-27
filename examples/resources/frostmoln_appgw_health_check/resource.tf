# A pool has at most one health check, so this resource is keyed on the pool.
# Writes are a REPLACE: an attribute you stop setting returns to the default.

resource "frostmoln_appgw_health_check" "web" {
  gateway_id = frostmoln_application_gateway.edge.id
  pool_id    = frostmoln_appgw_backend_pool.web.id

  protocol        = "http"
  path            = "/healthz"
  expected_status = "200"

  interval_seconds = 10
  timeout_seconds  = 3 # must be strictly less than interval_seconds

  healthy_threshold   = 2
  unhealthy_threshold = 3
}
