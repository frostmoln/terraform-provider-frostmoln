# By address. It must lie inside the gateway's own VPC range.
resource "frostmoln_appgw_backend" "web_1" {
  gateway_id = frostmoln_application_gateway.edge.id
  pool_id    = frostmoln_appgw_backend_pool.web.id
  address    = "10.0.1.10"
  port       = 8080
  weight     = 1
}

# By instance: the platform resolves the VPC address itself, so `address` must
# not be set.
resource "frostmoln_appgw_backend" "web_2" {
  gateway_id  = frostmoln_application_gateway.edge.id
  pool_id     = frostmoln_appgw_backend_pool.web.id
  source_kind = "instance"
  source_id   = frostmoln_instance.web.id
  port        = 8080
}

# Creating a backend does NOT open the path to it. See
# frostmoln_appgw_backend_authorization.
