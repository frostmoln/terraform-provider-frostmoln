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
