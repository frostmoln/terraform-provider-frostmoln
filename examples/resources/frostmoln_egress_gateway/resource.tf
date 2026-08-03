# Give a VPC outbound internet access with a dedicated public IP address as its
# source. Suitable when a partner needs a stable address to allow-list.
#
# A VPC has at most one egress gateway. A VPC without one has no outbound
# internet access — and, because they are reached over the same path, no DNS
# resolution and no managed-service connectivity either.
resource "frostmoln_egress_gateway" "example" {
  vpc_id = frostmoln_vpc.example.id
  mode   = "public_ip"
}

# The address outbound traffic appears to come from.
output "egress_source_address" {
  value = frostmoln_egress_gateway.example.source_address
}
