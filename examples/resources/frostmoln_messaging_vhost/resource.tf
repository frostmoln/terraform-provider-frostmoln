# A vhost on a managed messaging instance. Destroying it deletes every message
# in it, and is refused while a permission still names it.
resource "frostmoln_messaging_vhost" "orders" {
  instance_id = frostmoln_messaging_instance.broker.id
  name        = "orders"
}
