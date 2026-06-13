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

  description = "Internal ingress load balancer (private VIP only — the default scheme)"

  tags = {
    environment = "production"
  }
}

# A PUBLIC load balancer: attach a pre-allocated, unassociated floating IP to
# the VIP for external reachability. scheme and floating_ip_id are ForceNew
# (there is no in-place internal<->public migration). floating_ip_id is REQUIRED
# when scheme = "public" and must be omitted when scheme = "internal".
resource "frostmoln_floating_ip" "ingress" {}

resource "frostmoln_load_balancer" "public_web" {
  name           = "public-web-lb"
  vpc_id         = frostmoln_vpc.main.id
  subnet_id      = frostmoln_subnet.public.id
  scheme         = "public"
  floating_ip_id = frostmoln_floating_ip.ingress.id

  # floating_ip_address is computed (the attached FIP's public address).
}
