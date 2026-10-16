# Grant the user access to the vhost. Referencing both resources makes
# Terraform create them (and wait for them to be ready) first, and destroy the
# permission before either of them.
resource "frostmoln_messaging_permission" "billing_orders" {
  instance_id = frostmoln_messaging_instance.broker.id
  user        = frostmoln_messaging_user.billing.name
  vhost       = frostmoln_messaging_vhost.orders.name

  configure = "^billing\\." # declare and delete its own exchanges and queues
  write     = ".*"          # publish anywhere on the vhost
  read      = "^billing\\." # consume only its own queues
}

# A read-only consumer: "^$" grants nothing.
resource "frostmoln_messaging_permission" "audit_orders" {
  instance_id = frostmoln_messaging_instance.broker.id
  user        = frostmoln_messaging_user.audit.name
  vhost       = frostmoln_messaging_vhost.orders.name

  configure = "^$"
  write     = "^$"
  read      = ".*"
}
