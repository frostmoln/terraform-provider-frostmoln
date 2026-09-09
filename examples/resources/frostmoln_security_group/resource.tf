resource "frostmoln_security_group" "web" {
  name        = "web-sg"
  description = "Security group for web servers"
  vpc_id      = frostmoln_vpc.example.id

  # Delete the two allow-all egress rules the platform injects into every new
  # group (one per address family, empty remote prefix) as part of creating
  # it. Rules are additive and those defaults allow everything outbound, so
  # until they are gone the egress declared below narrows nothing. Opting in
  # here is also what a configuration wants ahead of provider v2, where this
  # becomes the default.
  delete_default_egress = true

  tags = {
    tier = "web"
  }
}

# Declare the group's egress explicitly. With the injected defaults deleted
# above, this is the egress the group actually carries — not an addition to
# an allow-all baseline.
resource "frostmoln_security_group_rule" "web_egress_https" {
  security_group_id = frostmoln_security_group.web.id
  direction         = "egress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_cidr       = "0.0.0.0/0"
  description       = "Allow HTTPS egress"
}
