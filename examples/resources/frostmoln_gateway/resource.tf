# A VPC's outbound internet path. A VPC has at most one gateway; without
# one it is an isolated network — no inbound, no outbound, and (because they are
# reached over the same path) no platform DNS resolution and no managed-service
# connectivity either. That is how "no connectivity" is expressed: this resource
# simply not declared. There is no "none" mode.
#
# "public_ip" is the only mode a configuration can set. The VPC gets its own
# outbound gateway, and instances in it can still have public IPs of their own.
#
# Note the spelling: Terraform takes the wire value "public_ip". The fm CLI also
# accepts the hyphenated "public-ip", so a value copied from a CLI command has
# to be rewritten with the underscore here.
#
# With no public_ip_id the platform draws the gateway an address itself. That
# address is NOT a public IP of yours — it has no id, it is not in your public
# IP list, it draws on none of your public IP quota, and nothing pins it, so it
# may change if the gateway is rebuilt. It is the right default when nothing
# outside the VPC needs to know what its traffic comes from.
resource "frostmoln_gateway" "example" {
  vpc_id = frostmoln_vpc.example.id
  mode   = "public_ip"
}

resource "frostmoln_public_ip" "egress" {
  lifecycle {
    prevent_destroy = true
  }
}

resource "frostmoln_gateway" "chosen_address" {
  vpc_id       = frostmoln_vpc.dns_published.id
  mode         = "public_ip"
  public_ip_id = frostmoln_public_ip.egress.id
}

# Terraform derives the ordering from that reference: the address is allocated
# before the gateway that egresses from it, and on teardown the gateway is
# changed or destroyed BEFORE the address is released.
#
# That ordering is correct AND it is why the address needs its own guard. Once
# the gateway is gone the platform has already handed the address back as an
# ordinary unattached address, so the release that follows looks — to the
# platform — like the release of something idle, and it succeeds. The provider
# refuses it from the recorded attachment instead, and asks for
# `acknowledge_address_loss = true` on the frostmoln_public_ip before it will
# give a VPC's outbound source address up.
#
# Look up an address that already exists — one already published in DNS, or
# already sitting in a partner's allow-list — instead of allocating a new one:
#
#   data "frostmoln_public_ip" "published" {
#     address = "203.0.113.10"
#   }
#
#   resource "frostmoln_gateway" "reuse" {
#     vpc_id       = frostmoln_vpc.dns_published.id
#     mode         = "public_ip"
#     public_ip_id = data.frostmoln_public_ip.published.id
#   }

# Removing this gateway, or changing its mode, takes the VPC's internet, DNS and
# managed-service connectivity down, so both are refused unless the intent is
# stated in the configuration:
#
#   acknowledge_connectivity_loss = true
#
# Add that line, `terraform apply` it, and only then destroy the resource or
# change its mode — then REMOVE THE LINE AGAIN. It is ordinary configuration, so
# leaving it in place keeps the resource permanently disarmed for the rest of
# its life: a later `terraform destroy -target`, a module removal, or a `vpc_id`
# change that forces replacement would then disconnect the VPC with nothing in
# the plan beyond an ordinary "will be destroyed" line.

# An associated public IP cannot exist without a gateway, so put the
# dependency on the PUBLIC IP, pointing at the gateway. Terraform then creates
# the gateway first (associating a public IP into a gateway-less VPC makes the
# platform attach one implicitly, and the explicit gateway would then collide
# with it) and destroys the public IPs first (the gateway cannot be removed
# while they still depend on it).
resource "frostmoln_public_ip" "example" {
  instance_id = frostmoln_instance.example.id

  depends_on = [frostmoln_gateway.example]
}

# The address outbound traffic appears to come from. Null while the gateway is
# detached or the address is not yet known. This is the value to give a partner
# for their allow-list.
output "egress_source_address" {
  value = frostmoln_gateway.chosen_address.source_address
}
