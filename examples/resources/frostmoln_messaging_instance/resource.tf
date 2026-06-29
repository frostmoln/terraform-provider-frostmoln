resource "frostmoln_messaging_instance" "broker" {
  name      = "my-broker"
  engine    = "lavinmq"
  version   = "2.3"
  flavor_id = "mq.gp1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "persistent"
}
