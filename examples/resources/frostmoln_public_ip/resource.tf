# A public IP attached to an instance. Destroying this releases the address.
#
# `instance_id` is what makes the ATTACHMENT here, and an attached address
# depends on the VPC having a gateway — which Terraform cannot see, because
# nothing in this resource refers to frostmoln_gateway. Left unordered the two
# run concurrently: on teardown the gateway usually goes first and its delete is
# refused ("Gateway is still in use", GATEWAY_IN_USE), and on create the
# attachment can land first, after which the platform attaches a gateway itself
# and an explicit one either collides with it (GATEWAY_EXISTS, if it pins a
# public_ip_id) or silently adopts it. depends_on states the order; it creates
# and releases nothing.
resource "frostmoln_public_ip" "example" {
  instance_id = frostmoln_instance.example.id

  tags = {
    service = "web"
  }

  depends_on = [frostmoln_gateway.partner_facing]
}

# AN ADDRESS SOMEONE ELSE DEPENDS ON — one in a partner's allow-list, published
# in DNS, or serving a VPC's outbound path — is worth protecting in the configuration
# itself.
#
# Destroying this resource RELEASES THE ADDRESS.
#
# The address does not come back: it returns to a shared regional pool and is
# re-issued to whoever asks for one next, possibly another tenant, within
# minutes. Re-running `terraform apply` does not restore it — it allocates a
# different one. There is no undo and no support request that recovers it.
#
# `prevent_destroy` is Terraform's own guard and the strongest one available,
# because it fails the PLAN. A module removal, a `terraform destroy -target`, or
# CI running `terraform destroy -auto-approve` all stop before anything is sent.
#
# To retire the address deliberately: remove the lifecycle block, apply, then
# destroy.
resource "frostmoln_public_ip" "partner_facing" {
  tags = {
    purpose = "partner allow-list"
  }

  lifecycle {
    prevent_destroy = true
  }
}

# This address is the VPC's outbound source address, so the whole VPC's traffic
# arrives from it. See frostmoln_gateway.
#
# NOTE THE ASYMMETRY WITH THE ADDRESS ABOVE. This one is NAMED by the gateway, so
# the gateway already depends on it and Terraform already sequences the two
# correctly — allocate first, and on teardown detach the gateway before the
# address is released. Adding a depends_on back to the gateway here would be a
# plan-time `Cycle:` error. The ordering above is needed only because an ATTACHED
# address has no such reference to derive the dependency from.
resource "frostmoln_gateway" "partner_facing" {
  vpc_id       = frostmoln_vpc.example.id
  mode         = "public_ip"
  public_ip_id = frostmoln_public_ip.partner_facing.id
}

# The provider ALSO refuses to release an address that is serving a VPC's outbound path
# unless the intent is stated:
#
#   acknowledge_address_loss = true
#
# That check reads the recorded attachment, so it still fires in the case the
# platform itself cannot catch: because the gateway above refers to this
# address, `terraform destroy` destroys the gateway first, the platform hands
# the address back as an ordinary unattached address, and the release that
# follows looks — to the platform — like the release of something idle.
#
# Add the line, apply it, destroy, and then REMOVE IT AGAIN. It is ordinary
# configuration, so a `true` left behind disarms the check for the rest of the
# resource's life. It is not a substitute for `prevent_destroy`, which fails
# earlier and covers more.

# What is holding the address. Read this, not `instance_id`: an address serving
# a VPC's outbound path has no instance and no port, so it looks idle from every other
# angle. "unknown" means the platform did not say — never read it as "free".
output "partner_facing_attachment" {
  value = frostmoln_public_ip.partner_facing.attachment.kind
}
