---
page_title: "Upgrading to tags on database, cache, messaging, webserver and application gateway resources"
subcategory: "Guides"
description: |-
  The managed database, cache, messaging, webserver and application gateway
  resources now take tags and honour the provider's default_tags. What the first
  plan after the upgrade shows, and why it changes nothing on the service.
---

# Upgrading to tags on database, cache, messaging, webserver and application gateway resources

These resources now have `tags` and `tags_all`, and the provider's
`default_tags` apply to them like to every other taggable resource:

- `frostmoln_postgres_instance`, `frostmoln_mysql_instance`, their read replicas
  (`frostmoln_postgres_read_replica`, `frostmoln_mysql_read_replica`) and backups
  (`frostmoln_postgres_backup`, `frostmoln_mysql_backup`);
- `frostmoln_redis_instance`, `frostmoln_valkey_instance`;
- `frostmoln_messaging_instance`;
- `frostmoln_apache_instance`, `frostmoln_nginx_instance`,
  `frostmoln_webserver_domain`, `frostmoln_webserver_deployment`;
- `frostmoln_application_gateway` and its `frostmoln_appgw_listener`,
  `frostmoln_appgw_route`, `frostmoln_appgw_backend_pool`,
  `frostmoln_appgw_backend`, `frostmoln_appgw_certificate`,
  `frostmoln_appgw_waf_policy` and `frostmoln_appgw_backend_authorization`.

The matching data sources expose `tags`.

## The first plan after the upgrade

If your provider block sets `default_tags`, the first `terraform plan` after the
upgrade shows **one in-place update per existing resource above**: the defaults
are added to its tags (`tags_all` is shown as known after apply). The update
writes the tags only. It replaces nothing, restarts nothing and reconfigures
nothing: no database, cache, broker or web server is touched, no deploy runs,
and an application gateway gets no new configuration generation and needs no
config apply. Without `default_tags`, and without `tags` in the configuration,
the plan is empty.

Keys already on a resource that the configuration does not set — added in the
portal, by the fm CLI, or by `fm tenant default-tags apply` — are kept on every
apply and appear only in `tags_all`.

Read replicas, backups, gateway children and webserver domains and deploys have
their OWN tags: they are not copied from the instance or gateway they belong to,
and changing the parent's tags does not change theirs.

## Resources that used to replace on every change

Read replicas, backups, application gateway listeners, routes, backends,
certificates and backend authorizations, and webserver domains had no in-place
update: every attribute replaced them. `tags` is the one attribute that now
updates them in place; every other attribute still replaces them.

## Limits

A resource takes at most 32 tags, the provider's `default_tags` and the tenant's
default tags included. Keys beginning `frostmoln_` or `frostmoln-` (any letter
case) are reserved. A configuration that breaks these is refused at plan time
where the provider can tell, and by the API otherwise.
