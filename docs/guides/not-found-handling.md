---
page_title: "Why a 404 no longer always removes a resource from state"
subcategory: "Guides"
description: |-
  The provider now distinguishes "the service says this resource is gone" from
  "the platform could not route the request", and only acts on the first. What
  changed, what you will see, and what to do about it.
---

# Why a 404 no longer always removes a resource from state

## What changed

The provider used to treat **any** HTTP 404 as "this resource no longer exists".
On a refresh it removed the resource from state; on a destroy it reported
success.

That was unsafe, because the Frostmoln API gateway also answers 404 for **any
path it cannot route** — during a rollout, after a configuration change, or if a
routing rule is lost. A provider that cannot tell the two apart reads a
platform-side routing failure as "all of your resources were deleted", and:

- `terraform plan` / `terraform apply` silently drops resources from state on an
  ordinary refresh, and
- `terraform destroy` prints **Destroy complete** while your infrastructure is
  still running — and still billing.

The provider now requires the 404 to carry a backing service's own error
envelope before acting on it. A routing 404 is reported as an error instead.

## What you will see

Where a run previously removed a resource silently, you may now get an error
such as:

```
Error: Failed to read frostmoln_instance

  PATH_NOT_ROUTED: no route found for path
```

or, on an operation that polls:

```
Error: timed out waiting for instance (last poll error: PATH_NOT_ROUTED: no route
found for path): context deadline exceeded
```

## What to do

**Retry.** This almost always means the platform could not route your request —
a deployment in progress, or a transient edge fault. The resource is intact and
the run is safe to repeat.

~> **Do not run `terraform state rm` to clear this error.** Removing the resource
from state is exactly the outcome this change exists to prevent: it orphans live
infrastructure that keeps running and keeps billing, with nothing tracking it. If
the error persists across retries, contact support before touching state.

`terraform state rm` **is** the right remedy in one narrow case: a resource you
know was deleted outside Terraform, whose backing service has not yet been
updated to send the newer error format. If you hit that, tell us which resource
type — it is a bug on our side, not yours.

## Which resources are affected

All of them. The change is in the shared API client, not in any one resource.

Resources backed by the identity service — `frostmoln_api_key`,
`frostmoln_iam_policy`, `frostmoln_iam_policy_attachment` and
`frostmoln_workload_identity_binding` — additionally require an identity service
new enough to send the flat error envelope. Against an older platform, a
genuinely deleted resource of those types will error rather than converge; that
is the one case where `terraform state rm` is correct.

## Background

The discriminator is the shape of the error body, not its status or its code:
the gateway's routing 404 and a service's resource 404 use the same code, so
only the envelope separates them.
