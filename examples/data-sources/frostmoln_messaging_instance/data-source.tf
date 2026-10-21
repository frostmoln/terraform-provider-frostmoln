data "frostmoln_messaging_instance" "broker" {
  id = "mq-abc123"
}

output "messaging_private_ip" {
  value = data.frostmoln_messaging_instance.broker.private_ip
}

# AMQPS: the endpoint every instance with a certificate serves. Verify the
# server against hostname with the frostmoln_ca_bundle data source.
output "messaging_amqps_port" {
  value = data.frostmoln_messaging_instance.broker.amqps_port
}

output "messaging_tls_enabled" {
  value = data.frostmoln_messaging_instance.broker.tls_enabled
}

output "messaging_hostname" {
  value = data.frostmoln_messaging_instance.broker.hostname
}

# require_tls true (the default for new instances): plaintext is refused and
# port / management_port are null.
output "messaging_require_tls" {
  value = data.frostmoln_messaging_instance.broker.require_tls
}

# The plaintext AMQP port, served only when require_tls is false.
output "messaging_plaintext_amqp_port" {
  value = data.frostmoln_messaging_instance.broker.require_tls ? null : data.frostmoln_messaging_instance.broker.port
}
