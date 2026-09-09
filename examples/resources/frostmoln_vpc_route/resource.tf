resource "frostmoln_vpc" "main" {
  name = "my-vpc"
  cidr = "10.0.0.0/16"
}

resource "frostmoln_subnet" "app" {
  name   = "app"
  cidr   = "10.0.1.0/24"
  vpc_id = frostmoln_vpc.main.id
  zone   = "falkenberg"
}

# The appliance the routes below point at.
resource "frostmoln_instance" "appliance" {
  name      = "appliance"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.app.id
}

# Reach a partner network through the appliance.
#
# The next hop is the appliance's COMPUTED private_ip, never a literal
# address: the reference is what lets the graph see that the route needs the
# instance (and, through it, the subnet) — a literal "10.0.1.10" plus
# depends_on says the same thing by hand, goes stale when the appliance is
# replaced, and can tie the instance and the route into a dependency cycle
# when the instance's own configuration references the route's VPC.
resource "frostmoln_vpc_route" "partner" {
  vpc_id      = frostmoln_vpc.main.id
  destination = "203.0.113.0/24"
  next_hop    = frostmoln_instance.appliance.private_ip
}

# Route the whole VPC through a VPN appliance, and keep the appliance's own
# tunnel working.
#
# Without the second route the appliance's encrypted packets to its remote peer
# match the default route and loop back to it. A route is matched on its
# DESTINATION only — a VPC router has no per-source routing — so the exception
# takes every instance in the VPC off the tunnel for that peer's address, not
# just the appliance.
#
# A default route captures ALL egress from the VPC. Public IPs on instances here
# stop serving while it exists: the reply is routed by this same table, and its
# destination is the internet client. DNS and managed services keep working.
resource "frostmoln_gateway" "main" {
  vpc_id = frostmoln_vpc.main.id
  mode   = "public_ip"
}

resource "frostmoln_vpc_route" "forced_tunnel" {
  vpc_id      = frostmoln_vpc.main.id
  destination = "0.0.0.0/0"
  next_hop    = frostmoln_instance.appliance.private_ip
}

resource "frostmoln_vpc_route" "tunnel_peer_exception" {
  vpc_id      = frostmoln_vpc.main.id
  destination = "198.51.100.7/32"

  # `internet` is a reserved token, not an address: "out this VPC's own internet
  # gateway". It is the one place a literal next_hop is still correct — no
  # attribute can express the route, because the platform's own default gateway
  # has no address a customer could name. It needs the gateway to exist, hence
  # the dependency — without it the write is refused with
  # ROUTE_NO_INTERNET_GATEWAY.
  next_hop = "internet"

  depends_on = [frostmoln_gateway.main]
}
