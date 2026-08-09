# Read a VPC's gateway without managing it — for example one that came
# with the VPC, or that the platform attached when a public IP was associated.
#
# vpc_id is required and must never be empty: an empty value would widen the
# lookup to every gateway in the tenant and return an unrelated VPC's, so the
# provider refuses it before any request is made.
data "frostmoln_gateway" "production" {
  vpc_id = frostmoln_vpc.production.id
}

# The address to hand a partner for their allow-list. It is dedicated to this
# VPC. Only an address named via `public_ip_id` is PINNED, though — a
# platform-drawn one can change if the gateway is rebuilt, so it is not one to
# publish.
output "production_egress_address" {
  value = data.frostmoln_gateway.production.source_address
}

# "legacy" means no stored record exists, so the provenance is UNKNOWN — read it
# as "unknown", never as "old".
output "production_egress_origin" {
  value = data.frostmoln_gateway.production.origin
}
