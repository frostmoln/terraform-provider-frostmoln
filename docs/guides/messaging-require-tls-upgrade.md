---
page_title: "Upgrading to require_tls on frostmoln_messaging_instance"
subcategory: "Guides"
description: |-
  frostmoln_messaging_instance gains require_tls, TLS-only brokers by default. What
  changes for new and existing instances, and what the first plan after the upgrade shows.
---

# Upgrading to `require_tls` on `frostmoln_messaging_instance`

`frostmoln_messaging_instance` has a new attribute, `require_tls`, and the
`frostmoln_messaging_instance` data source reports it too.

## New instances are TLS-only by default

When `require_tls` is omitted, the platform creates the broker with TLS
required (`require_tls = true`): it serves AMQPS on `amqps_port` (5671) and
HTTPS management on 15671 only, and the plaintext AMQP (5672) and HTTP
management (15672) ports are closed. Connect with the `amqps://` URI and
verify the server against the `frostmoln_ca_bundle` data source.

Set `require_tls = false` only for clients that cannot use TLS; the broker
then serves plaintext AMQP and HTTP management beside TLS.

`require_tls` is fixed when the instance is created. Changing it in the
configuration **replaces the instance** (a new broker, with new data, users and
credentials), and the plan says so.

## `port` and `management_port` are null on TLS-required instances

On an instance with `require_tls = true`, `port` and `management_port` are
**null**: the broker serves no plaintext port. A configuration that reads
them — an `output`, a connection string built from `port`, a security rule — gets
null for every new instance. Use `amqps_port` and the credentials' `uri`
instead, or keep `require_tls = false` for that instance.

## Existing instances

Instances created before the setting existed report `require_tls = false` and
keep serving plaintext; nothing changes on them, and `port` and
`management_port` keep their values.

The first refreshed plan after the upgrade reads `require_tls` into state and
shows no change, as long as the configuration omits it or sets `false`. Setting
`require_tls = true` on such an instance plans a replacement. A plan made with
`-refresh=false` against state written by an older provider never plans a
replacement for `require_tls`; if the configuration sets a value the instance
does not hold, the apply stops with an error on `require_tls` before changing
anything.
