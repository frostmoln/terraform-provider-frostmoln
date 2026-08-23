---
page_title: "Upgrading load balancers to the type attribute"
subcategory: "Guides"
description: |-
  The frostmoln_load_balancer provider_type attribute has been replaced by
  type, with the values l7 and l4. What to change, and why the plan should be
  empty afterwards.
---

# Upgrading load balancers to the `type` attribute

`frostmoln_load_balancer` previously took a `provider_type` attribute whose
values named the load-balancer implementation. It now takes **`type`**, with
the values **`l7`** and **`l4`** — the same two options the portal has always
labelled Layer 7 and Layer 4.

## What to change

Rename the attribute **and** change the value. Both halves are required:

```hcl
# before
resource "frostmoln_load_balancer" "web" {
  name          = "web-lb"
  vpc_id        = frostmoln_vpc.main.id
  subnet_id     = frostmoln_subnet.public.id
  provider_type = "amphora"   # or "ovn"
}

# after
resource "frostmoln_load_balancer" "web" {
  name      = "web-lb"
  vpc_id    = frostmoln_vpc.main.id
  subnet_id = frostmoln_subnet.public.id
  type      = "l7"            # "amphora" -> "l7", "ovn" -> "l4"
}
```

If you omitted `provider_type` entirely, omit `type` as well — the default is
unchanged in meaning.

## Why both halves

Renaming only the attribute leaves `type = "amphora"`, which the provider
**rejects** with a validation error listing the allowed values. That refusal is
deliberate. `type` forces replacement, and your state now holds the canonical
value, so a plan that carried `"amphora"` would compare unequal and propose to
**destroy and recreate the load balancer** — a new VIP and an outage. Failing
validation is the safe outcome; the provider will not let a half-finished
rename reach a plan.

## Your state migrates automatically

The provider carries a state upgrader. On the first command after the upgrade
it rewrites stored state from `provider_type` to `type`, mapping the old values
to the new ones. You do not need `terraform state` commands, and you should not
edit state by hand.

## Confirm before applying

After editing, run:

```console
$ terraform plan
```

For an unchanged load balancer this must report **no changes**. If it proposes
a replacement, stop and check the value: a replacement here means the config
and the migrated state disagree, and applying it would give the load balancer a
new VIP.
