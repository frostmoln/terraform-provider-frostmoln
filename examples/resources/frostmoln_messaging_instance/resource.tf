resource "frostmoln_messaging_instance" "broker" {
  name      = "my-broker"
  type      = "lavinmq"
  version   = "2.3"
  flavor_id = "mq.gp1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "persistent"

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}
