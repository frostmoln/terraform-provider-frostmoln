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

# Reach a partner network through an appliance running in the VPC.
#
# The next hop must be an address on a subnet attached to the VPC, so the route
# depends on the subnet: created the other way round, the write is refused with
# ROUTE_NEXT_HOP_UNREACHABLE.
resource "frostmoln_vpc_route" "partner" {
  vpc_id      = frostmoln_vpc.main.id
  destination = "203.0.113.0/24"
  next_hop    = "10.0.1.10"

  depends_on = [frostmoln_subnet.app]
}

# Route the whole VPC through a VPN appliance, and keep the appliance's own
# tunnel working.
#
# Without the second route the appliance's encrypted packets to its remote peer
# match the default route and loop back to it. A route is matched on its
# DESTINATION only — a VPC router has no per-source routing — so the exception
# takes every instance in the VPC off the tunnel for that peer's address, not
# just the appliance.
resource "frostmoln_gateway" "main" {
  vpc_id = frostmoln_vpc.main.id
  mode   = "public_ip"
}

resource "frostmoln_vpc_route" "forced_tunnel" {
  vpc_id      = frostmoln_vpc.main.id
  destination = "0.0.0.0/0"
  next_hop    = "10.0.1.10"

  depends_on = [frostmoln_subnet.app]
}

resource "frostmoln_vpc_route" "tunnel_peer_exception" {
  vpc_id      = frostmoln_vpc.main.id
  destination = "198.51.100.7/32"

  # `internet` is a reserved token, not an address: "out this VPC's own internet
  # gateway". It is the only way to write this route, because the platform's own
  # default route has no address a customer could name. It needs the gateway to
  # exist, hence the dependency — without it the write is refused with
  # ROUTE_NO_INTERNET_GATEWAY.
  next_hop = "internet"

  depends_on = [frostmoln_gateway.main]
}
