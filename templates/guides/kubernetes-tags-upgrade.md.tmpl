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
reach it, and the cluster resource does not manage that pool's tags. Change
them in the portal, with `fm kubernetes node-pool tags set`, or with the API.
Clusters created before this release have an untagged initial pool.

## Limits

A cluster or pool takes at most 32 tags, the provider's `default_tags` and the
tenant's default tags included. Keys beginning `frostmoln_` or `frostmoln-`
(any letter case) are reserved. A configuration that breaks these is refused at
plan time where the provider can tell, and by the API otherwise.
