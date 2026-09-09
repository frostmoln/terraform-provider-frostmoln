# An http listener that only redirects, and the https listener that serves.

resource "frostmoln_appgw_listener" "http" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "http"
  protocol   = "http"
  port       = 80

  redirect_to_https = true
}

resource "frostmoln_appgw_listener" "https" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "https"
  protocol   = "https"
  port       = 443

  default_certificate_id = frostmoln_appgw_certificate.www.id
  tls_min_version        = "1.2"
  tls_cipher_profile     = "modern"

  # Deny-by-default is NOT the behaviour here: omitting allowed_cidrs allows
  # every source. Name them to restrict.
  allowed_cidrs = ["0.0.0.0/0"]

  # Refuse traffic from countries you do not serve.
  geo_block_mode = "deny"
  geo_countries  = ["RU", "KP"]

  rate_limit_rps   = 200
  rate_limit_burst = 400
}

# A tcp listener forwards bytes at layer 4: no routes, no TLS termination, no
# request inspection. It names the ONE pool it forwards to, and the source-CIDR,
# geo and rate-limit controls above all still apply.
#
# rate_limit_rps counts CONNECTIONS here, not requests. For SMTP, where one
# connection carries a whole session, an HTTP-shaped figure is orders of
# magnitude too permissive.
resource "frostmoln_appgw_listener" "smtp" {
  gateway_id      = frostmoln_application_gateway.edge.id
  name            = "smtp"
  protocol        = "tcp"
  port            = 25
  backend_pool_id = frostmoln_appgw_backend_pool.mail.id

  rate_limit_rps   = 10
  rate_limit_burst = 20
}

# A RANGE, inclusive, at most 512 ports. Each connection is forwarded to the
# SAME port on the backend that the client connected to, so 50000-50100 reaches
# 50000-50100 on your servers -- which is why the pool's health check has to name
# a port of its own: those backends have no fixed port to probe.
#
# NOTE THE SECOND POOL. A ranged listener and a single-port listener cannot share
# one pool: the ranged one forwards to whatever port the client used, the
# single-port one forwards to the port each backend declares, and a pool holds
# one answer. Pointing both at `mail` above is accepted by the API and then
# refused at every configuration apply -- including applies that change something
# else entirely -- while both listeners have already opened their public ports.
resource "frostmoln_appgw_backend_pool" "ftp" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "ftp-data"
  protocol   = "tcp"
}

resource "frostmoln_appgw_listener" "ftp_passive" {
  gateway_id      = frostmoln_application_gateway.edge.id
  name            = "ftp-passive"
  protocol        = "tcp"
  port            = 50000
  port_range_end  = 50100
  backend_pool_id = frostmoln_appgw_backend_pool.ftp.id
}
