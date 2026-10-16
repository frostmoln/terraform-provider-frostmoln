# An AMQP-only application user. The platform generates the password; it is
# read once the user is ready.
resource "frostmoln_messaging_user" "billing" {
  instance_id = frostmoln_messaging_instance.broker.id
  name        = "billing-app"
}

# The user reaches nothing until a frostmoln_messaging_permission grants it a
# vhost. Hand the connection URL to the application:
output "billing_amqp_url" {
  sensitive = true
  value = format("amqps://%s:%s@%s:%d/%s", # pragma: allowlist secret
    frostmoln_messaging_user.billing.name,
    urlencode(frostmoln_messaging_user.billing.password),
    frostmoln_messaging_instance.broker.private_ip,
    frostmoln_messaging_instance.broker.amqps_port,
    frostmoln_messaging_vhost.orders.name,
  )
}
