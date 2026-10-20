data "frostmoln_messaging_permission" "billing_orders" {
  instance_id = "mq_B352H9idUJP6jTWtom31nW"
  user        = "billing-app"
  vhost       = "orders"
}

output "billing_orders_read" {
  value = data.frostmoln_messaging_permission.billing_orders.read
}
