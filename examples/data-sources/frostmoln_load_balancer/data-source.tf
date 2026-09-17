# Resolve a load balancer that Terraform did not create (provisioned in the
# portal or with `fm`) by NAME — no hardcoded UUID.
data "frostmoln_load_balancer" "edge" {
  name = "edge-https"
}

# The resolved load balancer feeds references elsewhere — e.g. the address an
# app server targets (the public address when one is attached, the VIP
# otherwise; no composed endpoint attribute exists on the wire).
locals {
  edge_address = coalesce(data.frostmoln_load_balancer.edge.public_ip_address, data.frostmoln_load_balancer.edge.vip_address)
}

# A lookup by id behaves identically for load balancers whose UUID is already
# known.
data "frostmoln_load_balancer" "by_id" {
  id = data.frostmoln_load_balancer.edge.id
}

# The optional vpc_id narrows a name lookup (sent as the vpcId filter) and is
# echoed back from the read.
data "frostmoln_load_balancer" "edge_internal" {
  name   = "edge-https"
  vpc_id = "vpc-1d2e3f4a-0000-0000-0000-000000000000"
}

# A name that matches nothing — or more than one load balancer — fails the
# read loudly rather than picking one for you. Listeners, pools, members and
# health monitors are referenced through their own resources
# (frostmoln_lb_listener, frostmoln_lb_pool, frostmoln_lb_member,
# frostmoln_lb_health_monitor); this resolver's job is the id they are
# addressed by.
