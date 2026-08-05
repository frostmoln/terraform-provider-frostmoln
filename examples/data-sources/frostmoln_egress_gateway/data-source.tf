# Read a VPC's egress gateway without managing it — for example one that came
# with the VPC, or that the platform attached when a public IP was associated.
#
# vpc_id is required and must never be empty: an empty value would widen the
# lookup to every gateway in the tenant and return an unrelated VPC's, so the
# provider refuses it before any request is made.
data "frostmoln_egress_gateway" "production" {
  vpc_id = frostmoln_vpc.production.id
}

# The address to hand a partner for their allow-list. Under "nat" it is a
# platform address shared with other VPCs; under "public_ip" it is dedicated to
# this VPC.
output "production_egress_address" {
  value = data.frostmoln_egress_gateway.production.source_address
}

# "legacy" means no stored record exists, so the provenance is UNKNOWN — read it
# as "unknown", never as "old".
output "production_egress_origin" {
  value = data.frostmoln_egress_gateway.production.origin
}
