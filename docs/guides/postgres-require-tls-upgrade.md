---
page_title: "Upgrading to require_tls on frostmoln_postgres_instance"
subcategory: "Guides"
description: |-
  frostmoln_postgres_instance gains require_tls, tls_enabled and hostname, with TLS required
  by default on new instances. What changes for new and existing instances, and what the first
  plan after the upgrade shows.
---

# Upgrading to `require_tls` on `frostmoln_postgres_instance`

`frostmoln_postgres_instance` has a new attribute, `require_tls`, and two
read-only ones, `tls_enabled` and `hostname`. The `frostmoln_postgres_instance`
data source and `frostmoln_postgres_read_replica` report all three.

## New instances

Once TLS for managed databases is available in the deployment, a new instance
gets a certificate from the Frostmoln customer CA, issued for its `hostname` (`<label>.db.<vpc-label>.<tenant-label>.<org-label>.fm.internal`,
resolvable inside its VPC). Connect with `sslmode=verify-full` against the
bundle from the `frostmoln_ca_bundle` data source.

When `require_tls` is omitted, the platform default applies: TLS required
(`require_tls = true`, plaintext sessions refused) once TLS for managed
databases is available in the deployment. While it is not, the instance is
created without TLS and reports `require_tls = false` and `tls_enabled = false`,
and a configured `require_tls = true` is refused with `REQUIRE_TLS_UNAVAILABLE`.

Set `require_tls = false` only for clients that cannot use TLS; the server then
accepts plaintext beside TLS. PostgreSQL serves both on the same `port`, so
`port` is never null.

`require_tls` is fixed when the instance is created. Changing it in the
configuration **replaces the instance** (a new database, with new data and
credentials), and the plan says so. It cannot be set together with
`restore_from`: a restore target takes the platform default, and `require_tls`
reads the value back after the restore.

A read replica has its own `hostname` and certificate, and always its
primary's `require_tls`.

For a high-availability instance `tls_enabled` can change after a standby
rebuild: it reads false while the rebuilt standby has no certificate, and true
again once a later rebuild restores one. A refresh picks it up; it never plans
a change of its own.

## Existing instances

Instances created before TLS report `require_tls = false`, `tls_enabled =
false` and a null `hostname`, and keep accepting plaintext; nothing changes on
them. They get no certificate later: create a new instance to connect with
`verify-full`.

The first refreshed plan after the upgrade reads the three attributes into
state and shows no change, as long as the configuration omits `require_tls` or
sets `false`. Setting `require_tls = true` on such an instance plans a
replacement. A plan made with `-refresh=false` against state written by an
older provider never plans a replacement for `require_tls`; if the
configuration sets a value the instance does not hold, the apply stops with an
error on `require_tls` before changing anything.
