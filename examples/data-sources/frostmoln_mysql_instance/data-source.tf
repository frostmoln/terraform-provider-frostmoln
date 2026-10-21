# Resolve a MySQL instance that Terraform did not create
# (provisioned in the portal or with `fm`) by NAME — no hardcoded id.
data "frostmoln_mysql_instance" "orders" {
  name = "orders"
}

# The resolved instance feeds references elsewhere — e.g. an allow-list or an
# application server's connection string (no composed endpoint attribute
# exists on the wire: compose host and port).
locals {
  orders_address = coalesce(data.frostmoln_mysql_instance.orders.public_ip, data.frostmoln_mysql_instance.orders.private_ip)
}

# A lookup by id behaves identically for instances whose id is already known.
data "frostmoln_mysql_instance" "by_id" {
  id = data.frostmoln_mysql_instance.orders.id
}

# A name that matches nothing — or more than one MySQL instance — fails
# the read loudly rather than picking an instance for you.

# TLS, as the platform reports it: require_tls (plaintext refused),
# tls_enabled (a certificate for hostname from the Frostmoln customer CA) and
# hostname (null on an instance built before TLS).
output "orders_tls" {
  value = {
    required = data.frostmoln_mysql_instance.orders.require_tls
    enabled  = data.frostmoln_mysql_instance.orders.tls_enabled
    hostname = data.frostmoln_mysql_instance.orders.hostname
  }
}
