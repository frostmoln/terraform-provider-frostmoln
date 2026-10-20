data "frostmoln_messaging_vhost" "orders" {
  instance_id = "mq_B352H9idUJP6jTWtom31nW"
  name        = "orders"
}

output "orders_vhost_status" {
  value = data.frostmoln_messaging_vhost.orders.status
}
