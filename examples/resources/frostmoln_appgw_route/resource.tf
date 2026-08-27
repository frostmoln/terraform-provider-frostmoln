# Routes hang off a listener, and PRIORITY IS EXPLICIT: lower wins.
#
# There is no implicit longest-prefix rule, because that would make one route's
# effect depend on another route's contents. Give the specific routes low
# numbers and the catch-all a high one.

resource "frostmoln_appgw_route" "api" {
  gateway_id  = frostmoln_application_gateway.edge.id
  listener_id = frostmoln_appgw_listener.https.id
  name        = "api"
  priority    = 100

  host            = "api.example.com"
  path_match_type = "prefix"
  path            = "/v1"
  backend_pool_id = frostmoln_appgw_backend_pool.api.id

  request_headers_set = {
    "X-Forwarded-Host" = "api.example.com"
  }
  request_headers_remove = ["X-Internal-Token"]
}

# The catch-all, deliberately last.
resource "frostmoln_appgw_route" "spa" {
  gateway_id  = frostmoln_application_gateway.edge.id
  listener_id = frostmoln_appgw_listener.https.id
  name        = "spa"
  priority    = 900

  path_match_type = "prefix"
  path            = "/"
  backend_pool_id = frostmoln_appgw_backend_pool.web.id
}
