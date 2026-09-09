resource "frostmoln_appgw_backend_pool" "web" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "web"
  protocol   = "http"
  algorithm  = "round_robin"
}

# Re-encrypt to the backend. tls_verify_backend is left to the platform default
# unless you have a reason: setting it to false silently disables certificate
# verification.
resource "frostmoln_appgw_backend_pool" "api" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "api"
  protocol   = "https"

  tls_verify_backend = true
  tls_server_name    = "api.internal"
  tls_ca_certificate = file("${path.module}/internal-ca.pem")

  session_affinity    = "cookie"
  session_cookie_name = "FMSESSION"

  timeout_connect_ms  = 2000
  timeout_response_ms = 30000
}

# A pool behind a tcp listener. proxy_protocol is what lets the backend see the
# real client address: at layer 4 there is no X-Forwarded-For, so without it
# every connection appears to come from the gateway and spam scoring, rate
# limiting and abuse logging on that server are all useless.
#
# Turn the backend's own PROXY-protocol option on FIRST. A server that is not
# expecting the header reads it as the first bytes of your protocol and every
# connection fails.
#
# protocol must be "http" and session_affinity must not be "cookie": the gateway
# serves a pool in one mode, and a cookie is an HTTP header it never writes at
# layer 4.
resource "frostmoln_appgw_backend_pool" "mail" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "mail"
  protocol   = "http"
  algorithm  = "least_connections"

  proxy_protocol = true
}
