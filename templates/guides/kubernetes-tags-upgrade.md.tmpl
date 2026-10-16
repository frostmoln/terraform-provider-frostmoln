---
page_title: "Upgrading to Kubernetes cluster and node pool tags"
subcategory: "Guides"
description: |-
  frostmoln_kubernetes_cluster and frostmoln_kubernetes_node_pool now take tags
  and honour the provider's default_tags. What the first plan after the upgrade
  shows, and why it touches no node.
---

# Upgrading to Kubernetes cluster and node pool tags

`frostmoln_kubernetes_cluster` and `frostmoln_kubernetes_node_pool` now have
`tags` and `tags_all`, and the provider's `default_tags` apply to them like to
every other taggable resource.

## The first plan after the upgrade

If your provider block sets `default_tags`, the first `terraform plan` after the
upgrade shows **one in-place update per existing cluster and node pool**: the
defaults are added to its tags (`tags_all` is shown as known after apply). The
update writes the tags only. It does not replace anything, does not restart the
control plane and does not touch a single node — resource tags are platform
metadata, not Kubernetes node labels. Without `default_tags`, and without `tags`
in the configuration, the plan is empty.

Keys already on a cluster or pool that the configuration does not set — added
in the portal, by the fm CLI, or by `fm tenant default-tags apply` — are kept on
every apply and appear only in `tags_all`.

## The initial node pool

The cluster's initial node pool got a **one-time copy** of the cluster's merged
tags when the cluster was created. Changing the cluster's `tags` later does not
reach it. Manage the pool's own tags with `initial_node_pool.tags` on
`frostmoln_kubernetes_cluster`; every tag the pool holds, the copied ones
included, is in `initial_node_pool.tags_all`. Keys you do not set there are kept.

The provider's `default_tags` apply to the initial pool as to any resource. A
cluster `tags` key that overrides a default stays the cluster's only: set it in
`initial_node_pool.tags` too to keep it on the pool.

Clusters created before the tags release have an untagged initial pool. With
`default_tags` set, the first plan after upgrading to a provider that manages
`initial_node_pool.tags` shows an in-place update of the pool's tags: a
tags-only write that touches no node. The same first plan rewrites a pool key
that a cluster tag overrode from a default (default `team = "ops"`, cluster
`team = "web"`): the pool's copy says `web`, and the pool gets the default `ops`.
Set the key in `initial_node_pool.tags` to keep `web` there.

## Limits

A cluster or pool takes at most 32 tags, the provider's `default_tags` and the
tenant's default tags included. Keys beginning `frostmoln_` or `frostmoln-`
(any letter case) are reserved. A configuration that breaks these is refused at
plan time where the provider can tell, and by the API otherwise.
