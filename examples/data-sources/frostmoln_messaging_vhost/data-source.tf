data "frostmoln_messaging_vhost" "orders" {
  instance_id = "51455a51-db3d-4231-ac27-4fee2553c15f"
  name        = "orders"
}

output "orders_vhost_status" {
  value = data.frostmoln_messaging_vhost.orders.status
}
