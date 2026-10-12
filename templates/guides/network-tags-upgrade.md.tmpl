---
page_title: "Upgrading to tags on gateways, DNS records and load balancer listeners and members"
subcategory: "Guides"
description: |-
  frostmoln_gateway, frostmoln_dns_record, frostmoln_lb_listener and
  frostmoln_lb_member now take
  tags and honour the provider's default_tags. What the first plan after the
  upgrade shows, and why it changes nothing on the network.
---

# Upgrading to tags on gateways, DNS records and load balancer listeners and members

These resources now have `tags` and `tags_all`, and the provider's
`default_tags` apply to them like to every other taggable resource:

- `frostmoln_gateway`;
- `frostmoln_dns_record`;
- `frostmoln_lb_listener`;
- `frostmoln_lb_member`.

The `frostmoln_gateway` data source exposes `tags`.

## The first plan after the upgrade

If your provider block sets `default_tags`, the first `terraform plan` after the
upgrade shows **one in-place update per existing resource above**: the defaults
are added to its tags (`tags_all` is shown as known after apply). The update
writes the tags only:

- a gateway's tags-only update changes no mode and no address, keeps every
  connection, and needs no `acknowledge_connectivity_loss`;
- a DNS record's tags-only update does not write the zone: no serial bump, and
  resolvers see nothing change;
- a listener's or pool member's update changes its tags and nothing else on
  the load balancer.

Without `default_tags`, and without `tags` in the configuration, the plan is
empty.

Keys already on a resource that the configuration does not set — added in the
portal, by the fm CLI, or by `fm tenant default-tags apply` — are kept on every
apply and appear only in `tags_all`.

A listener's or member's tags are its own: they are not copied from its pool or
load balancer, and changing the parent's tags does not change them.

## Limits

A gateway or DNS record takes at most 32 tags, the provider's `default_tags`
and the tenant's default tags included. Keys beginning `frostmoln_` or
`frostmoln-` (any letter case) are reserved. A configuration that breaks these
is refused at plan time where the provider can tell, and by the API otherwise.
