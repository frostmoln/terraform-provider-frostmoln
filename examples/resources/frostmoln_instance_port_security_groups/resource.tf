# Manage the security groups on a SINGLE port of a multi-NIC instance, leaving
# the instance's other ports untouched. Use this only when an instance's ports
# need DIFFERENT security-group sets; for one set applied to every port, use the
# security_groups attribute on frostmoln_instance instead.

resource "frostmoln_instance_port_security_groups" "frontend_nic" {
  instance_id = frostmoln_instance.example.id
  port_id     = "b1e0f6c2-1234-4a5b-9c8d-abcdef012345" # Neutron port ID (see the instance's per-port SG breakdown)

  security_groups = [
    frostmoln_security_group.web.id,
    frostmoln_security_group.ssh.id,
  ]
}
