---
page_title: "Secrets in Terraform state"
subcategory: "Guides"
description: |-
  Marking an attribute sensitive redacts CLI output — it does not redact the
  state file. Which Frostmoln attributes land in state in plaintext, where else
  they appear, and how to protect them.
---

# Secrets in Terraform state

## `sensitive` is a display control, not a protection

Terraform writes **every** attribute of **every** managed resource to state —
data source results too, and regardless of where the value came from. The only
exception is [write-only
arguments](https://developer.hashicorp.com/terraform/language/resources/ephemeral/write-only),
which this provider does not yet offer.

Marking an attribute `sensitive` does not change that. It redacts the value from
`plan` and `apply` output, prints `<sensitive>` for `terraform output`, requires
any output referencing it to be declared `sensitive = true`, and propagates to
values derived from it. All of that is about display. `terraform output -json`
and `terraform output -raw` still print the value, and the state file still
holds it verbatim.

You can see this for yourself:

```console
$ jq '.resources[] | select(.type=="frostmoln_api_key") | .instances[0].attributes.key' \
    terraform.tfstate
"<your API key, in cleartext>"
```

This is Terraform's behaviour, not a Frostmoln one — it applies to every
provider. **Anyone who can read your state file can read these values.**

## Which attributes are affected

### Credentials minted once by the platform

Returned at create and never again. They have to live in state for the resource
to be usable at all — there is nowhere else for them to come from.

| Resource | Attribute |
|---|---|
| `frostmoln_api_key` | `key` |
| `frostmoln_s3_credential` | `secret_access_key` |
| `frostmoln_container_registry_credential` | `secret` |

### A credential the platform re-issues

| Resource | Attribute |
|---|---|
| `frostmoln_kubernetes_cluster` | `kubeconfig` |

Unlike the three above, this one is re-fetchable: the provider pulls a fresh
kubeconfig from a dedicated endpoint on every refresh while the cluster is
running. It is in state only because this is a managed resource — which also
means your state holds a continually re-minted cluster-admin credential. If you
do not need it in Terraform, leave the attribute unreferenced and fetch it out
of band instead:

```console
$ fm kubernetes cluster kubeconfig <cluster-id>
```

### Values you supply in configuration

| Resource | Attribute |
|---|---|
| `frostmoln_instance` | `user_data`, `console_password` |
| `frostmoln_launch_template` | `user_data` |
| `frostmoln_secret` | `secret_value` |
| `frostmoln_appgw_certificate` | `private_key_pem` |

These are the attributes the provider marks `sensitive`. Every *other* value you
configure is in state too — a DNS TXT record holding a verification token is as
readable as anything in these tables.

~> **Where a value is consumed decides whether it is stored, not where it came
from.** A variable read only by the `provider` block never reaches state. A
variable assigned to *any* resource attribute — `secret_value`, `user_data`,
`private_key_pem` — is written to state in full, no matter how carefully it was
minted. Passing a secret through `var.` does not keep it out of the state file.

`user_data` is the one that surprises people: a cloud-init document is ordinary
configuration until you embed a token, a database password or a deploy key in
it — at which point that credential is in your state file too.

## Where else these values appear

The state file is not the only artifact that carries them:

- **Saved plan files.** `terraform plan -out=tfplan` writes every attribute into
  the plan, and records variable values — including the provider's own `api_key`,
  which is otherwise never persisted. Treat a CI plan artifact exactly like a
  state file: restrict who can download it, and delete it after the apply.
  `terraform show -json tfplan` prints the lot.
- **CI caches and workspaces** that keep state or plans between jobs.
- **`TF_LOG=DEBUG` / `TRACE` output**, which records the provider's HTTP
  responses — including the one carrying a newly minted credential — and
  `crash.log`.
- **`terraform.tfstate.backup`**, which is written next to local state and
  *survives* a later migration to a remote backend.

## Protecting the state file

**Use a remote backend with encryption at rest and access control**, rather than
leaving state on disk next to your configuration. Any backend your team already
runs is fine.

If you back it with Frostmoln object storage, mint the credential for it **out
of band** — `fm` or the portal, scoped to that one bucket — and hand it to the
backend through environment variables. Do not create it with the configuration
whose state lives in that bucket: its secret would land inside the very object it
protects, and because `frostmoln_s3_credential` is replace-only, re-scoping or
rotating it destroys the credential the backend is currently using. Enable
`versioning` on the state bucket while you are there.

Then:

- **Never commit `terraform.tfstate` or `terraform.tfstate.backup`** — add both
  to `.gitignore`, along with `tfplan` / `*.tfplan` and any `*.tfvars` holding
  secrets.
- **Restrict who can read the state bucket.** Reading state is equivalent to
  reading every credential in it.
- **Treat local state as a secret.** `terraform.tfstate` on a laptop or in a CI
  workspace deserves the same care as a private key.

### If a state file leaks

What to do differs by table, and for the third one the obvious move is
destructive.

- **Minted-once credentials** (`api_key`, `s3_credential`,
  `container_registry_credential`): **revoke, then re-mint.** Creating a new one
  does not disable the leaked one. All three are replace-only — there is no
  update route — so `terraform apply -replace=<address>` is the mechanism, and
  every consumer of the old credential needs updating.
- **`kubeconfig`:** there is no self-service revocation for a cluster-admin
  kubeconfig. Open a support case.
- **Values you supplied:** rotate the credential **at its source**, not the
  attribute. Editing `user_data` or `console_password` replaces the instance —
  ephemeral disk contents gone, new addresses — and a new `private_key_pem`
  replaces the certificate and interrupts the listener unless you attach it with
  `create_before_destroy`.
- **Purge the copies.** A versioned state bucket still holds the pre-rotation
  object, and `terraform.tfstate.backup` still sits on whichever machine ran the
  last local apply.

## Keeping secrets out of state in the first place

- **Don't put long-lived secrets in `user_data`.** Put a narrowly scoped,
  short-lived `frostmoln_api_key` there instead — one that can read the single
  `frostmoln_secret` the instance needs at boot, and nothing else. Scope it with
  `frostmoln_iam_policy`. This trades a long-lived secret in state for a small
  one; it does not eliminate the exposure, and there is no VM-side platform
  identity that would.
- **Reference, don't upload.** Where a resource takes an ID instead of the
  material — a listener attaching a certificate, for instance — the material
  never enters your state.
- **Scope what Terraform does create.** Give a `frostmoln_api_key` the narrowest
  permissions and the shortest life that still works, so a leaked state file
  costs you a rotation rather than an incident.

## Write-only arguments

Terraform 1.11 added write-only arguments: they reach the provider on apply and
are never persisted to the plan or the state. They are the mechanism that would
remove the third table above from state; they cannot help the first, because a
credential the platform mints once has to be stored somewhere for you to use it.
This provider does not offer write-only variants today.

If the exposure matters for a particular resource in your setup, raise it with
support — it helps us order the work.
