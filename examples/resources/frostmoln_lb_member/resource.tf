# A backend member of a load balancer pool.
resource "frostmoln_lb_member" "backend_1" {
  load_balancer_id = frostmoln_load_balancer.web.id
  pool_id          = frostmoln_lb_pool.backend.id
  address          = "10.0.1.20"
  protocol_port    = 8080
  weight           = 1

  # The member's own tags, separate from its pool's and load balancer's.
  # Merged with the provider's default_tags; changing them updates the member
  # in place.
  tags = {
    env = "prod"
  }
}
