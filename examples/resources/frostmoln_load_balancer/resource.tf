# An amphora load balancer.
#
# provider and flavor_id are ForceNew: there is no in-place migration between
# the amphora (default; full L7 + TLS) and ovn (L4-only, source-IP preserving,
# zero VM overhead) Octavia drivers. Switching providers destroys and recreates
# the load balancer. Choose amphora unless you specifically need OVN's
# source-IP-preserving L4 behaviour.
# provider_type is named that way because "provider" is a reserved Terraform
# attribute name.
#
# Import IDs for the load balancer and its child resources:
#   frostmoln_load_balancer:    <lb_id>
#   frostmoln_lb_listener:      <lb_id>/<listener_id>
#   frostmoln_lb_pool:          <lb_id>/<pool_id>
#   frostmoln_lb_member:        <lb_id>/<pool_id>/<member_id>
#   frostmoln_lb_health_monitor:<lb_id>/<pool_id>
resource "frostmoln_load_balancer" "web" {
  name          = "web-lb"
  vpc_id        = frostmoln_vpc.main.id
  subnet_id     = frostmoln_subnet.public.id
  provider_type = "amphora"

  description = "Public ingress load balancer"

  tags = {
    environment = "production"
  }
}
