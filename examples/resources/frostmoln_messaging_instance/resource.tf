resource "frostmoln_messaging_instance" "broker" {
  name      = "my-broker"
  type      = "lavinmq"
  version   = "2.3"
  flavor_id = "mq.gp1.small"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.private.id

  persistence_mode = "persistent"

  # TLS only (the default): plaintext AMQP and HTTP management are closed and
  # port / management_port are null. Fixed at create; set false only for
  # clients that cannot do TLS.
  require_tls = true

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

# The broker's certificate is issued for its hostname by the Frostmoln customer
# CA (trust the bundle from the frostmoln_ca_bundle data source and verify the
# server against this name). Null, with tls_enabled false, on an instance
# without a certificate.
output "broker_hostname" {
  value = frostmoln_messaging_instance.broker.tls_enabled ? frostmoln_messaging_instance.broker.hostname : null
}
