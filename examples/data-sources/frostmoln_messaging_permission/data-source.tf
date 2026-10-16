data "frostmoln_messaging_permission" "billing_orders" {
  instance_id = "51455a51-db3d-4231-ac27-4fee2553c15f"
  user        = "billing-app"
  vhost       = "orders"
}

output "billing_orders_read" {
  value = data.frostmoln_messaging_permission.billing_orders.read
}
