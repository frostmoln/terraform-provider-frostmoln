# An ISOLATED VPC. This is what a VPC is on its own: no outbound internet, no
# inbound, and — because they are reached over the same path — no platform DNS
# resolution and no managed-service connectivity either. Nothing is missing from
# this configuration; a VPC simply has no connectivity until one is declared for
# it, and there is no argument here that turns it on.
resource "frostmoln_vpc" "isolated" {
  name        = "isolated-vpc"
  description = "Backend VPC with no internet path"
  cidr        = "10.1.0.0/16"

  tags = {
    environment = "production"
  }
}

# A CONNECTED VPC. The outbound path is its own resource, frostmoln_gateway,
# pointed at the VPC. Declare it and instances in the VPC get internet egress,
# platform DNS resolution and managed-service connectivity; leave it out and they
# get none of the three — which is what makes a cloud-init step that installs
# packages or calls an external endpoint fail on first boot.
resource "frostmoln_vpc" "example" {
  name        = "production-vpc"
  description = "Production VPC"
  cidr        = "10.0.0.0/16"

  tags = {
    environment = "production"
  }
}

resource "frostmoln_gateway" "example" {
  vpc_id = frostmoln_vpc.example.id
  mode   = "public_ip"
}

# With no public_ip_id the platform draws the gateway an address itself — fine
# when nothing outside the VPC needs to know what its traffic comes from. Name a
# frostmoln_public_ip instead when a partner allow-list or a DNS record does.
# See the frostmoln_gateway resource for that, and for the
# acknowledge_connectivity_loss guard that removing a gateway requires.
