# A health monitor for a load balancer pool. A pool has at most one health
# monitor (singleton); the type must be tcp, http, or https (ping is not
# supported).
resource "frostmoln_lb_health_monitor" "backend" {
  load_balancer_id = frostmoln_load_balancer.web.id
  pool_id          = frostmoln_lb_pool.backend.id
  type             = "http"
  delay            = 5
  timeout          = 3
  max_retries      = 3
  url_path         = "/healthz"
  http_method      = "GET"
  expected_codes   = "200"
}
