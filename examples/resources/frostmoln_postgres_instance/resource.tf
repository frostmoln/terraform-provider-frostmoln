resource "frostmoln_postgres_instance" "main" {
  name       = "app-db"
  version    = "16"
  flavor_id  = "db.gp1.small"
  storage_gb = 50
  vpc_id     = frostmoln_vpc.main.id
  subnet_id  = frostmoln_subnet.db.id

  backup_enabled        = true
  backup_retention_days = 35

  # backup_schedule is deliberately NOT set: the platform picks one for the
  # instance's storage size and reports it back, so it shows as "known after
  # apply" on create and on any storage_gb change. Larger volumes need longer
  # between scheduled backups; set it yourself only when you want a specific
  # window, and the platform's refusal names the floor for your size.

  # Point-in-time recovery: any second inside the restorable window, restored
  # onto a NEW instance. Write it EXPLICITLY — the platform stamps it at
  # create and the stamp is permanent, so an instance created without it can
  # never be given it. PostgreSQL only, needs backup_enabled, not available
  # with ha_enabled.
  pitr_enabled = true

  # Extensions are platform catalog names, checked against the live catalog
  # at plan time. Preload-required ones (timescaledb, pg_stat_statements
  # in the catalog today) pay a restart window; the apply for the rest is
  # in place, with no restart. Requires the database-extensions entitlement
  # on the tenant. Omitting the attribute keeps whatever the instance has;
  # an explicit empty set (`extensions = []`) disables every extension.
  extensions = ["timescaledb", "pg_stat_statements"]

  # Customer-tunable wait budgets. Defaults: create/update/delete = 30m.
  # Raise for slow provisions (e.g. multi-VM HA pairs) without waiting on a
  # provider release; a timeouts change is an in-place no-op on live data.
  timeouts {
    create = "30m"
    update = "30m"
    delete = "30m"
  }
}

output "postgres_endpoint" {
  value = "${frostmoln_postgres_instance.main.private_ip}:${frostmoln_postgres_instance.main.port}"
}

output "postgres_restorable_window" {
  description = "When this instance can be restored to, as the platform reported it at the last refresh. latest_restorable_time advances continuously, so quote it, do not pin to it."
  value = {
    from   = frostmoln_postgres_instance.main.earliest_restorable_time
    to     = frostmoln_postgres_instance.main.latest_restorable_time
    paused = frostmoln_postgres_instance.main.pitr_archive_paused_reason
  }
}

# Restore to a point in time: a NEW instance, built from another one's
# backups. The source is untouched.
#
# The platform builds the target from the source's own shape, so version,
# flavor_id, vpc_id, subnet_id and ha_enabled must equal the source's;
# storage_gb may be larger (grown in place afterwards) but not smaller.
#
# restore_from is create-only, and records what Terraform asked for — the
# platform reports no such field. Removing it later clears that record in one
# in-place update and changes nothing on the platform; importing a restored
# instance records nothing. Changing it to a different source or instant
# REPLACES this resource, which destroys the restored database, and adding it
# to an instance that already exists is refused.
resource "frostmoln_postgres_instance" "recovered" {
  name       = "app-db-recovered"
  version    = frostmoln_postgres_instance.main.version
  flavor_id  = frostmoln_postgres_instance.main.flavor_id
  storage_gb = frostmoln_postgres_instance.main.storage_gb
  vpc_id     = frostmoln_postgres_instance.main.vpc_id
  subnet_id  = frostmoln_postgres_instance.main.subnet_id

  restore_from = {
    source_instance_id = frostmoln_postgres_instance.main.id
    point_in_time      = "2026-09-20T14:30:00Z"
  }
}
