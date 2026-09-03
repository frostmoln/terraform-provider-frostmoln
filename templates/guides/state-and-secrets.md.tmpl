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
which this provider offers for `frostmoln_secret.secret_value` — see
[Write-only arguments](#write-only-arguments) below.

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

| Resource | Attribute | Write-only form |
|---|---|---|
| `frostmoln_instance` | `user_data`, `console_password` | not yet |
| `frostmoln_launch_template` | `user_data` | not yet |
| `frostmoln_secret` | `secret_value` | `secret_value_wo` |
| `frostmoln_appgw_certificate` | `private_key_pem` | not yet |

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
are never persisted to the plan or the state. They are the mechanism for keeping
the third table above out of state; they cannot help the first, because a
credential the platform mints once has to be stored somewhere for you to use it.

One row of that table has a write-only form today — `frostmoln_secret`. Set
`secret_value_wo` instead of
`secret_value`, and pair it with `secret_value_wo_version`:

```terraform
resource "frostmoln_secret" "api_token" {
  name = "prod/payments/api-token"

  secret_value_wo         = var.payments_api_token
  secret_value_wo_version = "1"
}
```

Exactly one of `secret_value` and `secret_value_wo` must be set — the pair is
mutually exclusive, and a configuration must still supply one of them.
`secret_value_wo_version` is required alongside `secret_value_wo`.

An existing configuration keeps working untouched, because the write-only
attributes are purely additive and `secret_value` was only relaxed from required
to optional. The write-only form is opt-in, and only a configuration that opts in
needs Terraform 1.11 — support is negotiated per client, not pinned by the
provider. Setting `secret_value` while running 1.11 or later produces a warning
pointing at the write-only form.

`secret_value_wo_version` is a string, so a counter, a date, or an upstream
version identifier all work — whatever you change when you change the value.

~> **Do not derive it from the secret.** `secret_value_wo_version = sha256(var.token)`
makes rotation automatic, and it is tempting for exactly that reason. But the
version is deliberately *not* a sensitive attribute — it is the signal that tells
you why an update is happening, so it must stay readable — which means a digest of
your secret would be printed verbatim in `terraform plan` output: CI job logs, PR
plan comments, terminal scrollback. That is a wider audience than the state file,
and a digest lets anyone holding it confirm a guessed value offline or correlate
the same secret across environments. Use a counter.

**`secret_value_wo_version` is what makes updates work.** Because the value is
never stored, Terraform cannot tell that it changed: it has nothing to compare
against. Changing the version — any value; a counter or a date is typical —
is what makes the next apply send the current `secret_value_wo` to the platform
as a new secret version. Rotating the value without touching the version is a
no-op.

### What you give up

Terraform cannot see a write-only value at all — not just when *you* change it.
A secret rotated in the portal, by `fm`, or by any other client is invisible to
`terraform plan`, which reports no drift and will not correct it. The version
companion is a one-way signal from your configuration to the platform, not
two-way reconciliation. If you need Terraform to notice out-of-band edits, the
legacy `secret_value` is the attribute that does that, and the price is the
value in your state file.

Bumping `secret_value_wo_version` without changing the value writes a new secret
version regardless; versions count against `max_versions`, so a value rolled
often enough pushes older versions out of history.

### Migrating an existing secret

Removing `secret_value` and adding `secret_value_wo` + `secret_value_wo_version`
is an in-place update. Two things to expect:

- **It writes a new secret version**, even when the value is byte-identical.
  The version companion goes from unset to whatever you set, which is what tells
  the provider to push the value. The same applies after `terraform import`.
- **It does not un-expose the old value.** The value leaves your *current* state
  only. It remains in `terraform.tfstate.backup`, in every prior state version on
  a versioned remote backend, in any archived plan file, and in whatever state
  snapshots your colleagues have locally. Treat a secret that was ever set
  through `secret_value` as disclosed: **rotate it** — supply a genuinely new
  value through `secret_value_wo` — and prune old state versions per your
  backend. Switching the attribute is not a substitute for rotation.

The other attributes in the third table do not have a write-only form yet. If the
exposure matters for one of them in your setup, raise it with support — it helps
us order the work.
