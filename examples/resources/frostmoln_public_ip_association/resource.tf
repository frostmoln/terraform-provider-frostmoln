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
