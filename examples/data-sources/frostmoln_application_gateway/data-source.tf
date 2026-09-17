# Resolve an Application Gateway that Terraform did not create
# (provisioned in the portal or with `fm`) by NAME — no hardcoded UUID.
#
# The offer's child resources all take a required `gateway_id` with no other
# path to it, so this lookup is what lets a listener, backend pool, certificate
# or WAF policy attach to a gateway Terraform never created.
data "frostmoln_application_gateway" "edge" {
  name = "edge"
}

# The resolved id feeds the child resources.
resource "frostmoln_appgw_listener" "https" {
  gateway_id = data.frostmoln_application_gateway.edge.id
  name       = "https"
  protocol   = "https"
  port       = 443
}

# A lookup by id behaves identically for gateways whose UUID is already known
# (e.g. the gateway this configuration itself created).
resource "frostmoln_application_gateway" "self_served" {
  name      = "self-served"
  flavor_id = "agw.gp1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.edge.id
}
data "frostmoln_application_gateway" "by_id" {
  id = frostmoln_application_gateway.self_served.id
}

# A name that matches nothing — or more than one gateway — fails the read
# loudly rather than picking a gateway for you. The config apply trail is on
# the resolution: `config_generation` is what has been authored,
# `config_revision` what the appliance acknowledged, and when they differ
# there is a change the gateway is not yet serving.
output "gateway_config_trail" {
  value = [
    data.frostmoln_application_gateway.edge.config_generation,
    data.frostmoln_application_gateway.edge.config_revision,
    data.frostmoln_application_gateway.edge.config_status,
  ]
}

# The appliance's address composes like the resource's:
locals {
  gateway_public   = data.frostmoln_application_gateway.edge.public_ip
  gateway_private  = data.frostmoln_application_gateway.edge.private_ip
}
