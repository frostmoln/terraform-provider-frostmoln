---
page_title: "The surface contract: who owns what, in which shape"
subcategory: "Guides"
description: |-
  The rules the provider's resource surface follows: parents own child
  collections authoritatively and child members are separate per-ID resources,
  references point at computed attributes, platform-invented defaults carry an
  explicit policy, and secrets go in write-only arguments — plus what Terraform
  can and cannot see happen out of band.
---

# The surface contract: who owns what, in which shape

This guide is the contract the provider's resource surface follows, pinned so
it stops being a per-resource judgement call. Every point is the shape one or
more of the big-3 providers converged on the hard way; the citations are there
so the rule reads as evidence, not taste.

## 1. Two shapes, never mixed

A parent owns every child collection **authoritatively**; the members of that
collection are **separate per-ID resources**. A child collection is never
inlined into its parent's schema, and the two shapes are never mixed on the
same collection.

Evidence: the AWS provider documents that `aws_security_group`'s inline
`ingress`/`egress` blocks cannot be used together with the standalone
`aws_security_group_rule` resource — the two fight over the same rules and
produce perpetual diffs — and now recommends the standalone form. Azure's
`azurerm_network_security_group` carries the same warning about its inline
`security_rule` block versus the standalone `azurerm_network_security_rule`.
Both arrived at the same place: one collection, one owner, one shape.

In this provider the rule is mechanical, enforced by a test that walks every
resource's schema and fails on **any** inline child collection — not by name,
so the shape cannot slip back in as `members` or `endpoints`. The one
legitimate exception is a singleton configuration document whose whole
identity *is* the collection — the shape of AWS's
`aws_s3_bucket_cors_configuration` — where the members have no per-ID
existence. Today that is exactly the bucket CORS and lifecycle configuration
resources, and the exception list is checked in both directions so it cannot
grow silently.

## 2. Security group rules are separate per-ID resources

`frostmoln_security_group` manages the group and nothing else; every rule is
its own `frostmoln_security_group_rule`, one CIDR per rule.

Evidence: the AWS provider's own current guidance — avoid inline
`ingress`/`egress`, prefer one `aws_security_group_rule` per rule with a single
CIDR, and never mix the two shapes.

The group is still **authoritative over the set** in the only sense Terraform
can express: the rules this configuration declares are the rules it manages,
and a declared rule deleted out of band is detected on refresh and re-created.
What authority does *not* extend to is enumerated in the honesty matrix below.

## 3. Routes reference computed attributes, not literal addresses

Routes are separate per-ID resources (`frostmoln_vpc_route`), and a next hop
points at a **computed attribute** — `frostmoln_instance.appliance.private_ip`
— never a literal address the graph cannot see.

Evidence: AWS's `aws_route` takes its target by reference —
`network_interface_id`, `instance_id`, `gateway_id` — not as an address string,
so Terraform orders the route after the thing it points at.

The reference is what keeps the plan acyclic and correct: written as a literal
IP, the *value* dependency is invisible to the graph — `depends_on` can order
the resource, but nothing replans the route when the appliance is replaced and
its address changes, and two such literal edges tie into a dependency cycle
only a human can untangle. The one exception is the reserved token `internet`:
the platform's own gateway has no address a customer could name, so a literal
token is the only way to write that route, and it is read back as the token,
never as what it resolved to.

## 4. Platform-invented defaults carry an explicit policy

Some objects exist before you create anything — injected by the platform, not
by you. Each gets one of three policies, chosen per default:

| Policy | Meaning |
|--------|---------|
| **delete-on-create** | The default is not load-bearing; the resource removes it as part of Create. |
| **adopt-as-managed** | The default is load-bearing; a dedicated resource adopts it into management (AWS's `aws_default_vpc` / `aws_default_security_group` family). |
| **keep-with-docs** | The default stays; the surface documents that Terraform can see it but does not manage it. |

Evidence: AWS deletes the default allow-all egress rule when it creates a
security group (delete-on-create), and adopts the default VPC, security group,
route table and network ACL through the `aws_default_*` family instead of
fighting them (adopt-as-managed).

The Frostmoln defaults and their policies:

| Platform-invented default | Policy | Mechanism |
|---------------------------|--------|-----------|
| Allow-all egress pair on every new security group (IPv4 + IPv6, empty remote prefix, added by the network service) | **delete-on-create** | `delete_default_egress` on `frostmoln_security_group` — opt-in (default `false`) today; the default flips to `true` at provider v2, announced by a deprecation notice in the v1 line ahead of the flip. |
| Default VPC and default security group (`is_default`) | **keep-with-docs** | Readable as computed attributes; no `default_*` resource exists today. |
| Platform-owned VPC routes (the system routes DNS and managed services ride) | **keep-with-docs** | Invisible to `frostmoln_vpc_route` by design: not listed, cannot be created, cannot be imported. |
| Security groups provisioned for managed services | **keep-with-docs** | Reads work; every write is refused with `409` / `resource_in_use`, permanently. Do not import one. |

A configuration that declares its own egress and sets
`delete_default_egress = true` on its **new** groups today behaves exactly as
every new group will at v2 — opting in now is the migration path, not a
special case. Two caveats, both consequences of the attribute being
create-time only:

- Flipping the value on a group that **already exists** changes nothing (the
  plan warns to that effect): its rules stay as they are until you delete the
  pair outside Terraform or recreate the group.
- Ahead of the v2 flip, pin the attribute explicitly — `true` or `false` — on
  every group whose behaviour you have already chosen. At v2 the new default
  must apply only where state carries no value, so existing groups keep their
  recorded value; an unpinned group would otherwise adopt the flip with a
  no-op diff. The deprecation notice in the v1 line will say the same.

## 5. Secrets go in write-only arguments

Generated credentials and user-shaped secrets are passed as Terraform 1.11
write-only (`_wo`) arguments — `user_data_wo`, `console_password_wo`,
`secret_value_wo`, `private_key_pem_wo`, `password_wo` — which never reach
plan or state. The legacy plaintext attribute remains during the transition,
and each write-only argument comes with a `_wo_version` companion that carries
change detection: Terraform cannot see a write-only value change, so bumping
the version is what plans the replacement.

Evidence: write-only arguments are Terraform's own mechanism (1.11+) for
exactly this class of value.

## What Terraform can and cannot see — the honesty matrix

Refresh detects what a resource's Read fetches. Anything else is invisible —
not flagged in any plan, not removed by any apply or destroy. Per family:

| Resource family | Detected on refresh | Invisible to Terraform |
|-----------------|--------------------|------------------------|
| Security groups | Group changes or deletion; a **declared** rule deleted out of band (planned for re-creation) | A rule **added** out of band — by the portal, `fm`, the API, or a colleague. A `frostmoln_security_group_rules` data source sees them — but only where your configuration reads it: it lists one group's whole stored set, so a `check` block over it turns an addition into a failed plan; import what should be managed. The injected egress pair (unless deleted at create). |
| VPC routes | A **declared** route deleted out of band | A route added out of band, and all platform-owned routes. A `frostmoln_vpc_routes` data source sees them — but only where your configuration reads it: it lists the tenant-visible table, so a `check` block over it turns an addition into a failed plan. |
| Instances, volumes, and other single objects | Drift in every fetched attribute; deletion removes the resource from state | Nothing about the object itself — but children it owns follow their own row here. |

The rule of thumb: Terraform watches exactly what your configuration owns, by
ID. It cannot tell you the set grew. Reconciling additions means listing the
collection outside Terraform — `fm`, the portal or the API — and importing
each member that should be managed. The exceptions are the listing data
sources: `frostmoln_vpc_routes` lists the tenant-visible route table and
`frostmoln_security_group_rules` one group's whole stored rule set inside
Terraform, so a `check` block can turn an addition into a failed plan where
your configuration reads it.

## The never-mix rule, once more

If one thing survives from this guide: **one collection, one owner, one
shape.** Declare a child member as its own resource, or let the parent own the
collection — never both, never partly. Every perpetual diff and every
unplanned object in this surface's history traces back to breaking that rule,
which is why it is now enforced by a test rather than by convention.
