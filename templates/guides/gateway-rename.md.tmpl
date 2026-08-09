---
page_title: "Renaming frostmoln_egress_gateway to frostmoln_gateway"
subcategory: ""
description: |-
  The egress gateway resource was renamed to frostmoln_gateway. This is a
  resource-type rename, so it needs a state move as well as a config edit.
---

# Renaming `frostmoln_egress_gateway` to `frostmoln_gateway`

`frostmoln_egress_gateway` and its data source are now **`frostmoln_gateway`**.

The name changed because the resource was never egress-only. Attaching a public
IP to an instance creates a NAT rule on your VPC's own router, so **inbound
traffic already arrives through the same gateway that carries outbound**. The
old name implied a separate ingress gateway; there is none, and none is planned.

Nothing about the resource's behaviour, arguments or attributes changed. Only
the type name did.

## What you have to do

This is a **resource-type rename**, which Terraform cannot infer. Editing the
configuration alone makes Terraform plan a destroy of the old resource and a
create of the new one — and destroying a gateway takes that VPC off the internet.
Move the state first, then edit the configuration.

### 1. Move the state

For every gateway in your configuration:

```shell
terraform state mv frostmoln_egress_gateway.main frostmoln_gateway.main
```

If you use modules, include the module path:

```shell
terraform state mv \
  module.network.frostmoln_egress_gateway.main \
  module.network.frostmoln_gateway.main
```

List what you have first if you are unsure:

```shell
terraform state list | grep frostmoln_egress_gateway
```

### 2. Rename the type in your configuration

```diff
-resource "frostmoln_egress_gateway" "main" {
+resource "frostmoln_gateway" "main" {
   vpc_id = frostmoln_vpc.main.id
   mode   = "public_ip"
 }
```

Update every reference to it as well — `frostmoln_egress_gateway.main.id`
becomes `frostmoln_gateway.main.id`. Data sources move the same way.

### 3. Confirm

```shell
terraform plan
```

**A correct move plans no changes.** If you see a destroy and a create, the state
move did not happen or did not match — do not apply it. Re-check step 1.

## Error codes changed too

If you parse provider diagnostics, the platform's error codes lost their `EGRESS_`
prefix: `EGRESS_GATEWAY_IN_USE` is now `GATEWAY_IN_USE`, `EGRESS_MODE_UNAVAILABLE`
is now `GATEWAY_MODE_UNAVAILABLE`, and so on for the rest.

## Not affected

Security group rules still use `ingress` and `egress` for their `direction`
argument. That is traffic direction, not this resource, and it did not change.
