---
page_title: "Moving an egress gateway off the withdrawn nat mode"
subcategory: "Guides"
description: |-
  Upgrade guide: mode = "nat" on frostmoln_egress_gateway has been withdrawn.
  How to move an existing gateway to mode = "public_ip" in place, in three
  applies, without leaving the acknowledgement flag behind.
---

# Moving an egress gateway off the withdrawn `nat` mode

`mode = "nat"` on `frostmoln_egress_gateway` has been **withdrawn**. It sent a
VPC's outbound traffic through an address shared with other VPCs, and a VPC on
it could not give any of its instances a public IP — the platform needs the
VPC's own gateway to attach one. It is not coming back, and there is no third
mode: `public_ip` is the only value a configuration can set.

## What is affected, and what is not

**The gateway you already have is not broken.** A gateway created before the
withdrawal still reports `mode = "nat"`, still carries traffic, and the provider
still reads, refreshes and imports it — it records whatever mode the platform
reports and never rewrites it.

**Your configuration is what stops working.** The refusal is an attribute
validator, so it runs wherever Terraform validates the configuration:
`terraform validate` and `plan`, and the validation that `refresh` and `import`
do first. While a resource block still says `mode = "nat"`, all of them stop
with:

```
Error: Egress gateway mode "nat" has been withdrawn
```

So the fix is a configuration edit, and everything reads normally again as soon
as `mode` is no longer `"nat"` in the configuration.

## The move, in two applies

The change is applied **in place**. The gateway is never destroyed and rebuilt,
so its `id` survives — but it does re-address the VPC's egress and drop
connections in flight, which is why it needs an explicit acknowledgement.

### 1. Change the mode and acknowledge, in one edit

Set both at once. For a **mode change** the provider reads the acknowledgement
out of the plan, so it takes effect on the same apply — there is no need to
apply the flag on its own first, and you could not anyway: a block still
carrying `mode = "nat"` does not validate.

(A `terraform destroy` is the case that *does* need its own apply first, because
the destroy reads the flag out of state rather than the plan.)

```terraform
resource "frostmoln_egress_gateway" "example" {
  vpc_id = frostmoln_vpc.example.id
  mode   = "public_ip"

  acknowledge_connectivity_loss = true
}
```

`terraform apply`. What happens:

- The gateway is **PATCHed in place**; `id` is unchanged and there is no
  destroy/create in the plan.
- `source_address`, `status` and `origin` plan as `(known after apply)` — the
  platform re-records them.
- Outbound connectivity, platform DNS resolution and managed-service
  connectivity drop briefly while the path is rebuilt, and connections in flight
  are dropped. Every instance in the VPC egresses from a **different address**
  afterwards.

If anything outside the VPC allow-lists your old source address or resolves DNS
to it, plan for that before this apply — see *Choosing the address* below.

### 2. Remove the acknowledgement again

```terraform
resource "frostmoln_egress_gateway" "example" {
  vpc_id = frostmoln_vpc.example.id
  mode   = "public_ip"
}
```

**Do not leave the flag set.** It is ordinary configuration, so a `true` left
behind stays in state for the life of the resource and permanently disarms the
control: a later `terraform destroy -target`, a module removal, or a `vpc_id`
change that forces replacement then disconnects the VPC — no name resolution,
no managed-service connectivity — with nothing in the plan beyond an ordinary
"will be destroyed" line.

Removing it plans no other change, so this apply is a no-op against the
platform.

## Choosing the address

`mode = "public_ip"` gives the VPC **its own** outbound gateway. Which address
it uses is a separate decision:

- **Omit `public_ip_id`.** The platform draws the gateway an address itself.
  That address is *not* a public IP of yours: it has no id, it does not appear
  in your public IP list, it draws on none of your public IP quota, and nothing
  pins it — so it may change if the gateway is rebuilt. This is the right
  default when nothing outside the VPC needs to know what your traffic comes
  from. The `public_ip_id` attribute stays null.
- **Name a `public_ip_id`.** The gateway egresses from a
  `frostmoln_public_ip` of your own: listed, counted against your public IP
  quota like any other, and pinned as this gateway's source address. This is
  what a partner allow-list entry or a DNS record needs.

```terraform
resource "frostmoln_public_ip" "egress" {
  lifecycle {
    prevent_destroy = true
  }
}

resource "frostmoln_egress_gateway" "example" {
  vpc_id       = frostmoln_vpc.example.id
  mode         = "public_ip"
  public_ip_id = frostmoln_public_ip.egress.id

  acknowledge_connectivity_loss = true
}
```

Naming the address in the same edit as the mode change means one re-addressing
rather than two. Once a `public_ip_id` is set, removing the attribute again does
**not** release the address or hand the gateway back to a platform-drawn one —
an absent value resolves to the id already in state, and the API reads an absent
`publicIpId` as "keep the address this gateway has". To move the gateway
elsewhere, name the different address.

## If instances in the VPC have public IPs

An associated public IP cannot exist without an egress gateway, so a destroy
ordered the wrong way round fails with `EGRESS_GATEWAY_IN_USE`. That is a
destroy-ordering concern, not an upgrade one — the mode change itself is an
in-place PATCH and does not touch them. Put the dependency on the **public
IPs**, pointing at the gateway:

```terraform
resource "frostmoln_public_ip" "example" {
  instance_id = frostmoln_instance.example.id

  depends_on = [frostmoln_egress_gateway.example]
}
```

## If `mode` comes from a variable or module output

The provider refuses `"nat"` at validate time only when it can see the value. A
`mode` fed by a module output or another resource's attribute is unknown then,
and the refusal arrives from the API during the apply instead, as
`EGRESS_MODE_UNAVAILABLE`. Nothing is changed when it does. Fix the expression
that produces the value — there is no mode to fall back to.

## If the VPC should have no outbound path at all

There is no mode for that, and there never was. A VPC with **no** egress gateway
— this resource simply not declared — is an isolated network. Removing the
resource requires `acknowledge_connectivity_loss = true` in the configuration
first, exactly as the mode change does.
