---
page_title: "Upgrading to require_tls on frostmoln_redis_instance and frostmoln_valkey_instance"
subcategory: "Guides"
description: |-
  The Redis and Valkey resources gain require_tls, tls_enabled, hostname and tls_port, with TLS
  required by default on new instances, and port becomes null when TLS is required. What changes
  for new and existing instances.
---

# Upgrading to `require_tls` on `frostmoln_redis_instance` and `frostmoln_valkey_instance`

`frostmoln_redis_instance` and `frostmoln_valkey_instance` have a new
attribute, `require_tls`, and three read-only ones, `tls_enabled`, `hostname`
and `tls_port`. Their data sources report all four.

## New instances

Once TLS for managed caches is available in the deployment, a new instance
serves TLS on **`tls_port` (6380)** with a certificate from the Frostmoln
customer CA, issued for its `hostname`
(`<label>.cache.<vpc-label>.<tenant-label>.<org-label>.fm.internal`,
resolvable inside its VPC). Connect with `rediss://` on the hostname and
verify against the bundle from the `frostmoln_ca_bundle` data source, e.g.
`redis-cli --tls --cacert <bundle> --sni <hostname> -h <hostname> -p 6380`.
Redis and Valkey work the same way.

When `require_tls` is omitted, the platform default applies: TLS required
(`require_tls = true`) once TLS for managed caches is available. The plaintext
port 6379 is then **closed**, and `port` is **null**. While TLS is not
available, the instance is created without TLS and reports `require_tls =
false`, and a configured `require_tls = true` is refused with
`REQUIRE_TLS_UNAVAILABLE`.

Set `require_tls = false` only for clients that cannot use TLS: 6379 (`port`)
then stays open beside 6380 (`tls_port`).

**Check every reference to `port`.** On an instance that requires TLS it is
null, and an expression that interpolates it (`"${...private_ip}:${...port}"`)
fails. Pick the port by what the instance serves:
`tls_enabled ? tls_port : port`.

`require_tls` is fixed when the instance is created. Changing it in the
configuration **replaces the instance** (a new cache, with its data lost
unless restored), and the plan says so. A restored instance is a new instance
and takes the platform default.

## Existing instances

Instances created before TLS report `require_tls = false`, `tls_enabled =
false`, a null `hostname` and `tls_port`, and keep serving plaintext on
`port`; nothing changes on them. They get no certificate later.

The first refreshed plan after the upgrade reads the new attributes into state
and shows no change, as long as the configuration omits `require_tls` or sets
`false`. Setting `require_tls = true` on such an instance plans a replacement.
A plan made with `-refresh=false` against state written by an older provider
never plans a replacement for `require_tls`; if the configuration sets a value
the instance does not hold, the apply stops with an error on `require_tls`
before changing anything.
