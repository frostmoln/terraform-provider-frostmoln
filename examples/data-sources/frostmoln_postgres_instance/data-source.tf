# Resolve a PostgreSQL instance that Terraform did not create
# (provisioned in the portal or with `fm`) by NAME — no hardcoded UUID.
data "frostmoln_postgres_instance" "billing" {
  name = "billing"
}

# The resolved instance feeds references elsewhere — e.g. an allow-list or an
# application server's connection string (no composed endpoint attribute
# exists on the wire: compose host and port).
locals {
  billing_address = coalesce(data.frostmoln_postgres_instance.billing.public_ip, data.frostmoln_postgres_instance.billing.private_ip)
}

# A lookup by id behaves identically for instances whose UUID is already known.
data "frostmoln_postgres_instance" "by_id" {
  id = data.frostmoln_postgres_instance.billing.id
}

# A name that matches nothing — or more than one PostgreSQL instance — fails
# the read loudly rather than picking an instance for you.
