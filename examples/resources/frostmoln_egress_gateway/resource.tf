# A VPC's outbound internet path. A VPC has at most one egress gateway; without
# one it is an isolated network — no inbound, no outbound, and (because they are
# reached over the same path) no platform DNS resolution and no managed-service
# connectivity either.
#
# "nat" is the recommended mode: outbound traffic leaves under the platform's
# shared address and spends NONE of your tenant's public IPv4 quota. It is a
# per-region capability — a region that does not offer it yet refuses it, and
# the provider reports that as "not offered in this region yet" rather than as a
# configuration error.
resource "frostmoln_egress_gateway" "example" {
  vpc_id = frostmoln_vpc.example.id
  mode   = "nat"
}

# "public_ip" instead gives the VPC a dedicated, stable source address — what a
# partner needs in order to allow-list you — and spends one address from your
# tenant's public IP quota.
#
# Changing mode is applied IN PLACE (never a destroy/create), so the gateway id
# survives the change; the platform re-records the source address, which is why
# it plans as "(known after apply)".
#
# Note the spelling: Terraform takes the wire value "public_ip". The fm CLI also
# accepts the hyphenated "public-ip", so a value copied from a CLI command has
# to be rewritten with the underscore here.
#
# With no public_ip_id the platform allocates a public IP for the gateway. It is
# a real, listed, quota-counted resource of your tenant's and its id is recorded
# in `public_ip_id` — you simply did not choose which one.
resource "frostmoln_egress_gateway" "dedicated_address" {
  vpc_id = frostmoln_vpc.partner_facing.id
  mode   = "public_ip"
}

# Name the address yourself to make it one you MANAGE: the same address survives
# this gateway being rebuilt and the VPC being recreated, which is what a
# partner allow-list entry or a DNS record depends on.
#
# Changing public_ip_id later moves the VPC onto the other address in place —
# the gateway is never destroyed and rebuilt — but the address your traffic
# arrives from does change, so it needs the acknowledgement below just as a mode
# change does.
# prevent_destroy because the ADDRESS is what is irreplaceable here. Destroying
# this resource releases it to a shared regional pool, it is re-issued to
# whoever asks next, and re-applying allocates a DIFFERENT one — so the partner
# allow-list entry and the DNS record this whole arrangement exists to keep
# stable stop matching, permanently. See the frostmoln_public_ip examples.
resource "frostmoln_public_ip" "egress" {
  lifecycle {
    prevent_destroy = true
  }
}

resource "frostmoln_egress_gateway" "chosen_address" {
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
# give a VPC's egress address up.
#
# Look up an address that already exists — one already published in DNS, or
# already sitting in a partner's allow-list — instead of allocating a new one:
#
#   data "frostmoln_public_ip" "published" {
#     address = "203.0.113.10"
#   }
#
#   resource "frostmoln_egress_gateway" "reuse" {
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

# An associated public IP cannot exist without an egress gateway, so put the
# dependency on the PUBLIC IP, pointing at the gateway. Terraform then creates
# the gateway first (associating a public IP into a gateway-less VPC makes the
# platform attach one implicitly, and a later mode = "nat" gateway would collide
# with it) and destroys the public IPs first (the gateway cannot be removed
# while they still depend on it).
resource "frostmoln_public_ip" "example" {
  instance_id = frostmoln_instance.example.id

  depends_on = [frostmoln_egress_gateway.example]
}

# The address outbound traffic appears to come from. Null while the gateway is
# detached or the address is not yet known. This is the value to give a partner
# for their allow-list.
output "egress_source_address" {
  value = frostmoln_egress_gateway.chosen_address.source_address
}
