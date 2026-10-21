---
page_title: "Upgrading: tls_enabled is read-only on managed webservers"
subcategory: "Guides"
description: |-
  tls_enabled on frostmoln_nginx_instance and frostmoln_apache_instance is now read-only and
  reports whether the instance serves TLS. What changes in configurations and in state.
---

# Upgrading: `tls_enabled` is read-only on managed webservers

`tls_enabled` on `frostmoln_nginx_instance` and `frostmoln_apache_instance`
was accepted on create and update, but no managed webserver has ever served
TLS: every instance serves plain HTTP on `port`, whatever the attribute said.
It is now read-only. It reports whether the instance serves a certificate,
which no instance does yet, so it is `false`. The data sources report the same
value.

## Configurations

- `tls_enabled = true` is refused at plan time. Remove it from the
  configuration.
- `tls_enabled = false`, or no `tls_enabled` at all, plans as before. The
  provider never sends the attribute to the platform.

Serve HTTPS in front of a managed webserver through a
`frostmoln_application_gateway`.

A configuration that still says `tls_enabled = true` cannot be planned at all
with this version, not even for `terraform destroy`: remove the line first.

## Older provider versions

The platform now refuses `tls_enabled = true` on create and update. With a
provider version before this change, a configuration that sets
`tls_enabled = true` passes the plan and then fails at apply with
`tls_not_available`. Upgrade the provider, or remove the line.

## Existing state

No state migration is needed and nothing is replaced. An instance created with
`tls_enabled = true` records `true` in state; the platform reports `false` for
every instance, so the next refresh sets the attribute to `false`. Once
`tls_enabled = true` is removed from the configuration, the first plan after
the upgrade shows no change. With `-refresh=false` the stale `true` stays in
state until a refresh; an update of the instance is not refused for it, because
only a `tls_enabled` the configuration sets is checked.
