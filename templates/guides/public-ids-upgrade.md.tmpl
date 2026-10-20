---
page_title: "Upgrading to public ids"
subcategory: "Guides"
description: |-
  From this release the provider writes Frostmoln public ids (ins_…, vpc_…)
  instead of UUIDs. State migrates automatically. Read the first section before
  upgrading if any for_each is keyed on ids, or any count runs over a sorted
  list of ids.
---

# Upgrading to public ids

Frostmoln identifies resources by **public ids**: a short kind prefix and a
fixed-width body, for example `ins_8V9iHjoNmU8xYDupErMSNU` for an instance or
`vpc_4bzg7R3PQzPQnCUdYpaPTD` for a VPC. A public id is another spelling of the
UUID the resource always had. The API accepts either spelling, and both name
the same resource.

From this release the provider stores and reports public ids. Earlier releases
stored UUIDs.

## Before you upgrade: ids used as `for_each` keys or sorted `count` lists

This is the one case the provider cannot migrate for you.

- If a `for_each` map or set is **keyed on ids** (for example ids read from a
  data source), the keys change spelling with this release.
- If a `count` runs over a **sorted list of ids** (`sort(...)`), public ids sort
  in a different order from UUIDs, so the indexes shift.

Either way Terraform sees new addresses, so it plans to **destroy the instances
under the old addresses and create new ones**. A plain `count = 3` is not
affected.

Find `for_each` keys built from UUIDs before upgrading (this finds string keys
only; check sorted `count` lists by reading the configuration):

```console
$ terraform state list | grep '\["[0-9a-f]\{8\}-'
```

Each hit is an address keyed on a UUID, such as
`frostmoln_security_group_rule.allow["6f1c2a7e-0b8d-4e3a-9a51-2d4c8e7f1b30"]`.
For each one, add a `moved` block that maps the old key to the new one, then
plan:

```hcl
moved {
  from = frostmoln_security_group_rule.allow["6f1c2a7e-0b8d-4e3a-9a51-2d4c8e7f1b30"]
  to   = frostmoln_security_group_rule.allow["sg_EinDrepZgsHnfR1fkH6Q8w"]
}
```

The new key is the public id of the same resource. After the upgrade,
`terraform plan` shows it in the address it proposes to create, next to the
old address it proposes to destroy; copy both into the `moved` block.

The better fix is to key on something stable that is not an id, such as a
name. Then the spelling of an id never matters.

## Workspaces that pass ids to each other

If one configuration reads ids from another's outputs (`terraform_remote_state`,
or outputs copied into variables), upgrade the **reading** configuration first.

After the upgrade, a configuration's outputs carry public ids. A reader still on
an earlier provider then sees `vpc_…` in its configuration against the UUID in
its own state, and an earlier provider treats that as a different id, which
plans a **replacement**. This release treats the two spellings as one id, so a
reader on it is safe whichever form the outputs carry.

## Back up state first

The migration is one-way. `terraform plan` migrates state in memory only; the
first `apply` (or `apply -refresh-only`) writes it, with a newer schema version
that earlier provider versions refuse. Before that, save a copy:

```console
$ terraform state pull > pre-public-ids.tfstate
```

To roll back **before you have applied anything** with the new provider, pin a
0.87 release from 0.87.6 on (`version = "~> 0.87.6"`) and restore the copy with
`terraform state push -force pre-public-ids.tfstate`. Do not roll back after
applying changes: anything created or changed since the copy was taken would
no longer be tracked.

## What changes on its own

- **State migrates on the first command.** Every resource's schema version goes
  up by one. A state upgrader rewrites the ids it stores into public form. You
  do not need `terraform state` commands, and you should not edit state by hand.
- **References keep working.** `vpc_id = frostmoln_vpc.main.id` now carries a
  `vpc_…` id, and the referencing resource's migrated state holds the same
  value.
- **Composite ids migrate too**, for example `frostmoln_volume_attachment`
  (`<volume>/<instance>`) and `frostmoln_vpc_route` (`<vpc>/<destination>`).
  Import ids in either spelling keep working.
- **`frostmoln_appgw_health_check`'s id** is now `<gateway_id>/<pool_id>`. It
  was the health check's own row id, which public responses no longer carry.
- **`load_balancer_id` on `frostmoln_kubernetes_cluster`** (and its data
  source) is now null. It is an
  internal id of the cluster's control plane, with no public form.
- **The `frostmoln_volume` data source's `attachments[].id`** is removed. It
  was a block-storage attachment row id with no public form. Each row's
  `volume_id` and `instance_id` still identify the attachment.

## UUIDs written in your configuration

A UUID written literally in HCL keeps working, and **never causes a
replacement**. The provider compares ids by what they name, not how they are
spelled. Such an attribute may show one in-place update the first time, which
changes nothing on the platform and sends no request. After that the plan is
empty. You can replace the literal with the public id whenever convenient.

## Confirm before applying

After upgrading, run:

```console
$ terraform plan
```

For unchanged infrastructure the plan should report **no changes**, apart from
the `moved` addresses above. Then run `terraform apply -refresh-only` to write
the migrated state. If it proposes to replace a resource, stop:
replacement is never the expected outcome of this upgrade. Do not apply.
Contact support with the plan output. If you need to go back to the previous
provider meanwhile, follow the rollback steps above.
