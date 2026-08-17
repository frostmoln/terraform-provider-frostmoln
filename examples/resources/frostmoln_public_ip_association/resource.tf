# Attach an address you ALREADY have to an instance.
#
# The address here was reserved earlier — it is published in DNS and sits in a
# partner's allow-list — so the configuration must NOT allocate a new one, and
# must not release this one when the instance goes away. The data source looks
# the address up without managing it; this resource manages only the attachment.
data "frostmoln_public_ip" "published" {
  address = "203.0.113.10"
}

resource "frostmoln_instance" "web" {
  name      = "web-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.example.id
  subnet_id = frostmoln_subnet.example.id
}

resource "frostmoln_public_ip_association" "web" {
  public_ip_id = data.frostmoln_public_ip.published.id
  instance_id  = frostmoln_instance.web.id
}

# Replacing the instance moves the address to the new one: every configurable
# attribute forces replacement, so Terraform disassociates and re-associates. The
# ADDRESS is never released — `terraform destroy` on this configuration leaves
# 203.0.113.10 allocated to the tenant, so the DNS record and the partner's
# allow-list entry keep pointing at something that is still yours.
#
# The address IS off the instance between those two calls, and
# `create_before_destroy` cannot close that gap: it would create the new
# association while the old one still holds the address, and one address serves
# one thing at a time — the platform refuses the second attach with
# `409 Public IP is already associated` and the apply fails.

# A multi-NIC instance: `port_id` chooses WHICH interface answers on the address.
# Leave it unset (as above) and it resolves to the instance's first network port,
# which is what a single-homed instance wants. The port must belong to
# `instance_id`; anything else is refused before a request is sent.
data "frostmoln_public_ip" "backoffice" {
  address = "203.0.113.11"
}

resource "frostmoln_public_ip_association" "web_backoffice_nic" {
  public_ip_id = data.frostmoln_public_ip.backoffice.id
  instance_id  = frostmoln_instance.web.id
  port_id      = "b1e0f6c2-1234-4a5b-9c8d-abcdef012345" # the instance's second network port
}

output "web_public_ip" {
  value = data.frostmoln_public_ip.published.address
}

# --------------------------------------------------------------------------
# DO NOT DO THIS. `frostmoln_public_ip.instance_id` and
# frostmoln_public_ip_association are mutually exclusive: both manage the same
# attachment, so each apply undoes the other's and the plan never settles.
#
#   resource "frostmoln_public_ip" "wrong" {
#     instance_id = frostmoln_instance.web.id   # <- one manager
#   }
#
#   resource "frostmoln_public_ip_association" "also_wrong" {
#     public_ip_id = frostmoln_public_ip.wrong.id
#     instance_id  = frostmoln_instance.web.id  # <- a second manager of the same thing
#   }
#
# When the same configuration allocates the address AND the attachment is
# supposed to die with it, use `frostmoln_public_ip.instance_id` alone. Use this
# resource when the address is pre-existing, or has to outlive the instance.
# --------------------------------------------------------------------------

# Allocating here and attaching with this resource is fine too — as long as the
# allocating resource leaves `instance_id` unset, there is still only one
# manager of the attachment. This is what to reach for when the address must
# survive instance replacement within one configuration.
resource "frostmoln_public_ip" "api" {
  tags = {
    purpose = "api endpoint"
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "frostmoln_public_ip_association" "api" {
  public_ip_id = frostmoln_public_ip.api.id
  instance_id  = frostmoln_instance.web.id
}

# ORDERING AGAINST THE VPC'S GATEWAY. Declared in its own VPC on purpose: every
# association above would need the same line if it shared this gateway's VPC, and
# an example that quietly omitted it would teach the opposite of what it says.
#
# An attached address depends on the VPC having a gateway, and Terraform cannot
# see that — no attribute here refers to frostmoln_gateway, so the two are
# unordered and run concurrently. On teardown the gateway usually goes first and
# its delete is refused ("Gateway is still in use", GATEWAY_IN_USE), stopping the
# destroy half way. On create the attachment can land first, and the platform
# attaches a gateway ITSELF to carry it — after which an explicit gateway that
# pins a public_ip_id is refused ("VPC already has a gateway", GATEWAY_EXISTS),
# and one that pins none is NOT refused: it adopts the platform's gateway, and
# the VPC egresses from an address nobody chose.
#
# The depends_on belongs on the resource that makes the ATTACHMENT — this one.
# Not on frostmoln_public_ip.ordered: an address that is merely allocated depends
# on nothing, and where the address comes from the data source above there is no
# allocation resource to hang it on at all. Written the other way about (on the
# gateway, listing the addresses) it reverses both orders and makes the teardown
# fail every time rather than sometimes.
#
# Ordering is not the whole teardown: removing a gateway also needs
# `acknowledge_connectivity_loss = true` applied first — see frostmoln_gateway,
# and remove the line again once the destroy is done.
resource "frostmoln_vpc" "ordered" {
  name = "ordered-example"
  cidr = "10.20.0.0/16"
}

resource "frostmoln_subnet" "ordered" {
  vpc_id = frostmoln_vpc.ordered.id
  name   = "ordered-example"
  cidr   = "10.20.1.0/24"
}

resource "frostmoln_gateway" "ordered" {
  vpc_id = frostmoln_vpc.ordered.id
  mode   = "public_ip"
}

resource "frostmoln_instance" "ordered" {
  name      = "ordered-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.ordered.id
  subnet_id = frostmoln_subnet.ordered.id
}

resource "frostmoln_public_ip" "ordered" {
  tags = {
    purpose = "ordering example"
  }
}

resource "frostmoln_public_ip_association" "ordered" {
  public_ip_id = frostmoln_public_ip.ordered.id
  instance_id  = frostmoln_instance.ordered.id

  depends_on = [frostmoln_gateway.ordered]
}
