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

# `port` probes somewhere other than the backend's own port -- a mail server
# answering SMTP on 25 with a health endpoint on 8080.
#
# It is REQUIRED for a pool behind a RANGED tcp listener: those backends are
# forwarded to on whatever port the client used, so the probe has none to dial.
#
# Unlike every other attribute here it is not remembered from state: removing it
# from your configuration returns the probe to the backend's own port.
resource "frostmoln_appgw_health_check" "mail" {
  gateway_id = frostmoln_application_gateway.edge.id
  pool_id    = frostmoln_appgw_backend_pool.mail.id

  protocol = "tcp"
  port     = 8080

  # Naming a probe port opts the probe OUT of the pool's connection settings --
  # including the PROXY protocol header that pool sends on port 25. Leave this
  # off when the health endpoint beside the real service does not parse the
  # header, which is the usual case; set it true when it does. `false` here does
  # NOT mean "no header on probes": a probe with no `port` of its own inherits
  # the pool's setting and carries the header already.
  proxy_protocol = false

  interval_seconds = 10
  timeout_seconds  = 3
}
