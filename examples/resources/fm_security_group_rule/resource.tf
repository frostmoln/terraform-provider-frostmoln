resource "fm_security_group_rule" "https_ingress" {
  security_group_id = fm_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_cidr       = "0.0.0.0/0"
  description       = "Allow HTTPS from anywhere"
}

resource "fm_security_group_rule" "ssh_ingress" {
  security_group_id = fm_security_group.web.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_cidr       = "10.0.0.0/8"
  description       = "Allow SSH from internal network"
}
