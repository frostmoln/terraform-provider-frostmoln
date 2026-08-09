# Look up one of your tenant's public IPs without managing it. Reading one never
# allocates an address and never attaches it to anything.

# By address. This is what lets a configuration name an address that ALREADY
# exists — one published in DNS, or already sitting in a partner's allow-list —
# so a VPC can be given that exact address as its outbound source without
# importing the address into Terraform's management.
data "frostmoln_public_ip" "published" {
  address = "203.0.113.10"
}

resource "frostmoln_gateway" "partner_facing" {
  vpc_id       = frostmoln_vpc.example.id
  mode         = "public_ip"
  public_ip_id = data.frostmoln_public_ip.published.id
}

# By id.
data "frostmoln_public_ip" "by_id" {
  id = "pip-0f2c8e1a"
}

# What is holding the address. Read this rather than inferring "unused" from an
# absent private IP: an address that is a VPC's outbound source has no instance
# and no port, so it looks idle from every other angle.
#
#   "none"           allocated, nothing using it
#   "port"           attached to an instance or load balancer
#   "gateway" it is a VPC's outbound source address
output "published_address_is_used_for" {
  value = data.frostmoln_public_ip.published.attachment.kind
}
