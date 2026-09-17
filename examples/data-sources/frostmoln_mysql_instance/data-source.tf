# Resolve a MySQL instance that Terraform did not create
# (provisioned in the portal or with `fm`) by NAME — no hardcoded UUID.
data "frostmoln_mysql_instance" "orders" {
  name = "orders"
}

# The resolved instance feeds references elsewhere — e.g. an allow-list or an
# application server's connection string (no composed endpoint attribute
# exists on the wire: compose host and port).
locals {
  orders_address = coalesce(data.frostmoln_mysql_instance.orders.public_ip, data.frostmoln_mysql_instance.orders.private_ip)
}

# A lookup by id behaves identically for instances whose UUID is already known.
data "frostmoln_mysql_instance" "by_id" {
  id = data.frostmoln_mysql_instance.orders.id
}

# A name that matches nothing — or more than one MySQL instance — fails
# the read loudly rather than picking an instance for you.
