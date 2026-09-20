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

# Point-in-time recovery, as the platform reports it. pitr_capable is fixed
# when the instance was created and is what tells "turned off" apart from
# "cannot be turned on"; the window is null when there is none right now.
output "billing_pitr" {
  value = {
    enabled = data.frostmoln_postgres_instance.billing.pitr_enabled
    capable = data.frostmoln_postgres_instance.billing.pitr_capable
    # Kept as separate values, NOT interpolated into one string: both are null
    # whenever there is no window right now, and Terraform refuses a null inside
    # a string template.
    restorable_from = data.frostmoln_postgres_instance.billing.earliest_restorable_time
    restorable_to   = data.frostmoln_postgres_instance.billing.latest_restorable_time
    paused_why      = data.frostmoln_postgres_instance.billing.pitr_archive_paused_reason
  }
}
