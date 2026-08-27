# An Application Gateway: one Public IP fronting many services in your VPC.
#
# Nothing you attach to it is live until the gateway applies its configuration.
# `config_generation` is what has been authored; `config_revision` is what the
# appliance has acknowledged.

data "frostmoln_appgw_flavors" "available" {}

resource "frostmoln_application_gateway" "edge" {
  name      = "edge"
  flavor_id = "agw.gp1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.public.id

  # The default. The platform draws an address from the pool and RELEASES it
  # when the gateway is destroyed.
  public_ip_mode = "allocated"
}

# Bring your own address instead. It is used as-is and is NEVER released when
# the gateway is destroyed.
resource "frostmoln_application_gateway" "byo" {
  name      = "edge-byo"
  flavor_id = "agw.gp1.medium"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.public.id

  public_ip_mode = "selected"
  public_ip_id   = frostmoln_public_ip.edge.id
}
