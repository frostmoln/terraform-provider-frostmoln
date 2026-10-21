---
page_title: "Upgrading: TLS on frostmoln_nginx_instance and frostmoln_apache_instance"
subcategory: "Guides"
description: |-
  Managed webservers serve HTTPS on 443 for their internal hostname once TLS for managed
  webservers is available; require_tls decides whether port 80 only redirects. tls_enabled turns
  true on its own and must not be set.
---

# Upgrading: TLS on `frostmoln_nginx_instance` and `frostmoln_apache_instance`

`frostmoln_nginx_instance` and `frostmoln_apache_instance` have a new
attribute, `require_tls`, and a read-only `hostname`; the read-only
`tls_enabled` now turns `true`. The data sources report all three.

## New instances

Once TLS for managed webservers is available in the deployment, **every** new
instance serves HTTPS on 443 with a certificate from the Frostmoln customer
CA, issued for its internal `hostname`
(`<label>.web.<vpc-label>.<tenant-label>.<org-label>.fm.internal`, resolvable
inside its VPC only), whatever the configuration says. Verify it against the
bundle from the `frostmoln_ca_bundle` data source. The certificate carries the
internal name only: public HTTPS (a public name) is the
`frostmoln_application_gateway`'s, or your own certificate's.

`require_tls` decides port 80 only:

- omitted or `false` (the default): the site is served on port 80 beside
  HTTPS on 443;
- `true`: port 80 only redirects to https.

`port` stays the HTTP port (80) either way. A configured `require_tls = true`
while TLS is not available in the deployment is refused
(`require_tls_unavailable`). `require_tls` is fixed when the instance is
created: changing it **replaces the instance**, and the plan says so.

## `tls_enabled`

`tls_enabled` is read-only and reports whether the instance serves a
certificate: `true` on a new instance once the create completes. It carries
no plan modifier, so a refresh or an update records the platform's value.

- `tls_enabled = true` is refused at plan time, as before.
- `tls_enabled = false` is now refused at plan time on a **new** instance (the
  platform would contradict it), and refused at apply (warned at plan) on an
  instance that serves a certificate. On an instance built before TLS it still
  plans. Remove the attribute from the configuration.

## Existing instances

Instances created before TLS report `tls_enabled = false`, `require_tls =
false` and a null `hostname`, and keep serving plain HTTP on `port`; nothing
changes on them, and they get no certificate later. The first refreshed plan
after the upgrade shows no change, as long as the configuration omits
`require_tls` or sets `false`; `require_tls = true` plans a replacement. A
plan made with `-refresh=false` against state written by an older provider
never plans a replacement for `require_tls`; a configured value the instance
does not hold stops the apply with an error before anything changes.
