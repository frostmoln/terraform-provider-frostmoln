data "frostmoln_messaging_instance" "broker" {
  id = "mq-abc123"
}

output "messaging_private_ip" {
  value = data.frostmoln_messaging_instance.broker.private_ip
}

output "messaging_amqp_port" {
  value = data.frostmoln_messaging_instance.broker.port
}

output "messaging_amqps_port" {
  value = data.frostmoln_messaging_instance.broker.amqps_port
}

output "messaging_management_port" {
  value = data.frostmoln_messaging_instance.broker.management_port
}
