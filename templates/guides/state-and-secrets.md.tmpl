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
which this provider offers for `frostmoln_secret.secret_value`,
`frostmoln_instance.user_data` and `frostmoln_launch_template.user_data` — see
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
| `frostmoln_instance` | `user_data`, `console_password` | `user_data_wo`; `console_password` not yet |
| `frostmoln_launch_template` | `user_data` | `user_data_wo` |
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

~> **A write-only value skips the state file and the plan, not the wire.** It
still crosses the Terraform-to-provider RPC (visible under `TF_LOG=TRACE`) and
still travels in the outbound HTTPS request body. If you are adopting `_wo` to
satisfy an auditor, that is the boundary it moves: out of the durable artifacts,
not out of the request. The platform edge redacts these fields from its own
request logging.

~> **`_wo` keeps a value out of *your state*, not out of the *platform*.** Where
the API returns the stored value on read — `frostmoln_secret` and
`frostmoln_launch_template` both do — anyone with read access to the tenant can
still fetch it through the portal, `fm`, or an API key, and it is in the HTTPS
*response* body on every `plan` and `refresh`, so `TF_LOG=DEBUG` or above
captures it on every run rather than only on applies. The provider does not
decode or store it, but the bytes cross the wire. `frostmoln_instance.user_data`
is the exception: compute's instance response carries no user data at all.

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
  attribute. On a `frostmoln_launch_template`, note that editing `user_data`
  changes only the template: every instance already launched from it keeps — and
  keeps running — the leaked document. Editing `user_data` or `console_password`
  on an instance replaces it —
  ephemeral disk contents gone, new addresses — and a new `private_key_pem`
  replaces the certificate and interrupts the listener unless you attach it with
  `create_before_destroy`.
- **Purge the copies.** A versioned state bucket still holds the pre-rotation
  object, and `terraform.tfstate.backup` still sits on whichever machine ran the
  last local apply.

## Keeping secrets out of state in the first place

- **Use `user_data_wo`** — on `frostmoln_instance` and on
  `frostmoln_launch_template` — so the cloud-init document never reaches state at
  all. That covers the document; the next bullet still applies to what is *in* it.
- **Don't put long-lived secrets in `user_data`.** Put a narrowly scoped,
  short-lived `frostmoln_api_key` there instead — one that can read the single
  `frostmoln_secret` the instance needs at boot, and nothing else. Scope it with
  `frostmoln_iam_policy`. This trades a long-lived secret in state for a small
  one; it does not eliminate the exposure, and there is no VM-side platform
  identity that would. Note the key itself is minted by the platform, so it is in
  state whichever way you deliver it.
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

Three attributes have a write-only form today: `frostmoln_secret.secret_value`,
`frostmoln_instance.user_data` and `frostmoln_launch_template.user_data`. Each is
set instead of the legacy attribute and paired with a `_wo_version` companion.

### `frostmoln_secret.secret_value_wo`

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

### What you give up with `secret_value_wo`

Terraform cannot see a write-only value at all — not just when *you* change it.
A secret rotated in the portal, by `fm`, or by any other client is invisible to
`terraform plan`, which reports no drift and will not correct it. The version
companion is a one-way signal from your configuration to the platform, not
two-way reconciliation. If you need Terraform to notice out-of-band edits, the
legacy `secret_value` is the attribute that does that, and the price is the
value in your state file.

That last trade-off is specific to `frostmoln_secret`. Neither `user_data`
attribute detects drift in *either* form — the provider never reads the document
back — so on those two there is nothing to give up: staying on the legacy
attribute buys you the state exposure and no drift detection at all.

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

### `frostmoln_instance.user_data_wo`

The same pair, with one difference that matters: **changing
`user_data_wo_version` replaces the instance.**

```terraform
resource "frostmoln_instance" "worker" {
  name      = "worker-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id

  user_data_wo         = file("${path.module}/cloud-init.yaml")
  user_data_wo_version = "1"
}
```

`user_data` and `user_data_wo` are mutually exclusive, and — unlike the secret
pair — setting neither is fine: an instance does not need user data. Everything
`user_data` documents about the document itself still applies, including writing
it as plain text rather than `base64encode(...)`.

Replacement is not a quirk of the write-only form. A guest reads user data once,
at first boot, so `user_data` has always been create-only: the only way a new
document takes effect is a new instance. The version companion inherits that,
which means bumping it destroys the running instance — ephemeral disk contents
gone, new addresses — exactly as editing `user_data` does today. Editing the
document without touching the version changes nothing at all.

~> **`user_data_hash` is null on this path.** The hash is computed from the
configured `user_data`, and on the write-only path there is no configured value
in state to hash — nor should there be: a digest of the document is a digest of
whatever the document carries. `user_data_wo_version` is the change signal
instead, which is also why it must not be derived from the document.

`terraform import` leaves `user_data_wo_version` unset, so the first apply
against an imported instance plans a **replacement** — set the version to
whatever you want to call what the instance was launched with, or accept the
rebuild. This is the same trap `user_data` and `instance_access` already have on
an imported instance, and it is worth knowing before you import a production VM.

Migrating an existing instance means removing `user_data` and adding the pair,
and that plans a **replacement** — the same one any `user_data` edit plans. As
with the secret, switching the attribute does not un-expose the old document: it
remains in `terraform.tfstate.backup`, in prior remote-state versions and in
archived plan files. Treat anything it embedded as disclosed and rotate it at its
source.

### `frostmoln_launch_template.user_data_wo`

The same pair again, and this one updates **in place** — the version companion
sends the new document without destroying anything.

```terraform
resource "frostmoln_launch_template" "worker" {
  name      = "worker-template"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.example.id

  user_data_wo         = file("${path.module}/cloud-init.yaml")
  user_data_wo_version = "1"
}
```

`user_data` and `user_data_wo` are mutually exclusive, and setting neither is
fine — a template does not need user data. Everything `user_data` documents
about the document itself still applies, including writing it as plain text
rather than `base64encode(...)`.

Changing `user_data_wo_version` updates the template in place and nothing else
happens: **instances already launched from the template keep the user data they
were created with**, exactly as they do when you edit `user_data`. A new document
reaches a guest only when a new instance is launched from the template.

~> **Do not derive the version from the document.** `user_data_wo_version =
filemd5("cloud-init.yaml")` looks like it gives you automatic change detection,
and it does — at the cost of printing a digest of the document verbatim in
`terraform plan` output, CI job logs and PR plan comments. The version companion
is deliberately not `sensitive`, because it is the signal telling you why an
update is happening. A digest is also an offline confirmation oracle: it lets
someone test a guessed document, and correlate the same document across
environments. Use a counter, a date or a release tag.

`terraform import` leaves both `user_data` and `user_data_wo_version` unset, so
the first apply against an imported template sends the configured document again
as an ordinary in-place update. Harmless here — unlike `frostmoln_instance`,
where the same gap plans a replacement — but it does mean the plan shows an
update you did not ask for.

~> **Removing the attribute does not remove the document.** Deleting
`user_data`, or the write-only pair, sends nothing — a null value means "not the
source here", never "clear it" — so the apply is clean, state goes to null, and
the platform keeps serving the old document to every instance launched from the
template afterwards. To actually clear it, set `user_data = ""`, or destroy the
template. The same is true of `frostmoln_secret` and `frostmoln_instance`.

Migrating an existing template means removing `user_data` and adding the pair.
That plans a normal in-place update, and as with the others it does not un-expose
the old document: it remains in `terraform.tfstate.backup`, in prior remote-state
versions and in archived plan files. Treat anything it embedded as disclosed and
rotate it at its source.

The other attributes in the tables above do not have a write-only form yet. If
the exposure matters for one of them in your setup, raise it with support — it
helps us order the work.
