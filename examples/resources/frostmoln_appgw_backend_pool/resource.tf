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
