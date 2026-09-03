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
which this provider offers for every attribute in the third table below:
`frostmoln_secret.secret_value`, `frostmoln_instance.user_data`,
`frostmoln_instance.console_password`, `frostmoln_launch_template.user_data` and
`frostmoln_appgw_certificate.private_key_pem` — see [Write-only
arguments](#write-only-arguments) below.

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
| `frostmoln_instance` | `user_data`, `console_password` | `user_data_wo`, `console_password_wo` |
| `frostmoln_launch_template` | `user_data` | `user_data_wo` |
| `frostmoln_secret` | `secret_value` | `secret_value_wo` |
| `frostmoln_appgw_certificate` | `private_key_pem` | `private_key_pem_wo` |

Every attribute in this table now has a write-only form. That does not make the
legacy attribute obsolete — the write-only form needs Terraform 1.11 or later,
and on `frostmoln_secret` it costs you drift detection. See [Write-only
arguments](#write-only-arguments) for the trade-offs before switching.

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
- **`TF_LOG_SDK_PROTO_DATA_DIR`**, if set. It dumps the raw `ApplyResourceChange`
  request, and write-only values *are* present in that config — this is the one
  artifact the write-only form does not keep them out of.

~> **A write-only value skips the state file and the plan, not the wire.** It
still crosses the Terraform-to-provider RPC (visible under `TF_LOG=TRACE`) and
still travels in the outbound HTTPS request body. If you are adopting `_wo` to
satisfy an auditor, that is the boundary it moves: out of the durable artifacts,
not out of the request.

The gateway redacts `consolePassword` and `userData` from its own request logging
— but **not** `privateKeyPem` or `secretValue`. Where the gateway is configured
to log request bodies, an uploaded certificate key or a secret value is written
to the access log in full. If you are adopting `_wo` for an auditor, that log is
a durable artifact with a wider and longer-lived access set than the state file
you just hardened; ask us about it rather than assuming the wire is clean.

~> **`_wo` keeps a value out of *your state*, not out of the *platform*.** Where
the API returns the stored value on read — `frostmoln_secret` and
`frostmoln_launch_template` both do — anyone with read access to the tenant can
still fetch it through the portal, `fm`, or an API key, and it is in the HTTPS
*response* body on every `plan` and `refresh`, so `TF_LOG=DEBUG` or above
captures it on every run rather than only on applies. The provider does not
decode or store it, but the bytes cross the wire. `frostmoln_instance` is the
verified exception in the other direction: `consolePassword` and `userData`
appear nowhere in compute's instance response — the field does not exist
service-side — so nothing comes back on a refresh to capture. For
`frostmoln_appgw_certificate` we claim only what the provider controls: it has no
field to decode a key into, so nothing the API returns can reach your state.

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

- **Use the write-only form of whatever you are supplying.** `user_data_wo` on
  `frostmoln_instance` and `frostmoln_launch_template`, `console_password_wo` on
  `frostmoln_instance`, `secret_value_wo` on `frostmoln_secret`,
  `private_key_pem_wo` on `frostmoln_appgw_certificate` — none of them reaches
  state at all. That covers the value; the next bullet still applies to what is
  *inside* a cloud-init document.
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

Five attributes have a write-only form: `frostmoln_secret.secret_value`,
`frostmoln_instance.user_data`, `frostmoln_instance.console_password`,
`frostmoln_launch_template.user_data` and
`frostmoln_appgw_certificate.private_key_pem`. Each is set instead of the legacy
attribute and paired with a `_wo_version` companion.

Whether bumping that companion updates in place or replaces the resource is a
property of the resource, not of the write-only form: it replaces wherever the
legacy attribute was already create-only. `frostmoln_secret` and
`frostmoln_launch_template` update in place; `frostmoln_instance` (both
attributes) and `frostmoln_appgw_certificate` replace.

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
pair — setting neither is fine: an instance does not need user data. Omitting the
attribute is the only way to say that, though: `user_data_wo = ""` is refused at
plan time, because an empty document is not "no user data" on the write-only path
— it is a replacement that boots the new instance with no cloud-init and shows no
diff. Everything `user_data` documents about the document itself still applies,
including writing it as plain text rather than `base64encode(...)`.

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

### `frostmoln_instance.console_password_wo`

The same pair as `user_data_wo`, on the same resource and with the same
lifecycle: **changing `console_password_wo_version` replaces the instance.**

```terraform
resource "frostmoln_instance" "worker" {
  name      = "worker-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id

  console_password_wo         = var.console_password
  console_password_wo_version = "1"
}
```

`console_password` and `console_password_wo` are mutually exclusive, and setting
neither is fine — that is what leaves the console with no password. As with
`user_data_wo`, `console_password_wo = ""` is refused — at plan time when the
value is known then, at apply time when it is not; omit the attribute instead.

The replacement is not a quirk of the write-only form either. The platform seeds
the password through cloud-init at first boot and there is no API route that
changes it afterwards, so `console_password` has always been create-only — the
only way a new password takes effect is a new instance. Bumping the version
therefore destroys the running instance exactly as editing `console_password`
does today, and editing the password without touching the version changes
nothing at all.

Both write-only pairs can be set on the same instance. They are independent
signals: bumping either replaces the instance, and the replacement is launched
with whatever both write-only attributes currently hold.

~> **A console password is not a rotation mechanism.** Neither form gives you
one — the write-only pair only removes the state exposure. If you need to change
the password on a *running* instance, do it in the guest; Terraform's only
spelling is a rebuild, either way.

Migrating an existing instance means removing `console_password` and adding the
pair, and that plans a **replacement** — the running VM is destroyed, ephemeral
disk contents with it, and the replacement comes up on new addresses. Do it
deliberately, and not across a fleet in one apply.

It also does not un-expose the old password: it remains in
`terraform.tfstate.backup`, in prior remote-state versions and in archived plan
files. Treat a password that was ever set through `console_password` as
disclosed, and set a genuinely **new** one through `console_password_wo` rather
than moving the same value across — you are rebuilding the instance regardless,
so the new password costs nothing extra.

### `frostmoln_appgw_certificate.private_key_pem_wo`

The certificate API has no update operation, so this pair replaces too — and here
that is the *only* thing it could do.

```terraform
resource "frostmoln_appgw_certificate" "www" {
  gateway_id = frostmoln_application_gateway.edge.id

  # Give the name a distinct value per generation, so create_before_destroy has
  # a name to create under while the old certificate is still attached.
  name = "www-${var.certificate_version}"

  chain_pem = var.certificate_chain

  private_key_pem_wo         = var.certificate_key
  private_key_pem_wo_version = var.certificate_version

  lifecycle {
    create_before_destroy = true
  }
}
```

Exactly one of `private_key_pem` and `private_key_pem_wo` must be set — the pair
is mutually exclusive, and a certificate with no key is not a certificate.
`private_key_pem_wo_version` is required alongside `private_key_pem_wo`, and
`private_key_pem_wo = ""` is refused — at plan time when the value is known then,
at apply time when it is not.

`private_key_pem` was `required` before the write-only form existed and is now
`optional`. That is not a breaking change: an existing configuration keeps
working untouched, and only a configuration that opts in needs Terraform 1.11.
Setting `private_key_pem` while running 1.11 or later produces a warning pointing
at the write-only form.

**`create_before_destroy` is worth more here than on the other resources.**
Every attribute of this resource already forces a replacement, so rotating a
certificate has always meant creating a new one and destroying the old — but on
the write-only path bumping the version is the *only* way to push a new key, and
a listener whose certificate is destroyed before its replacement exists is a
listener serving nothing.

~> **The provider cannot put a returned key into your state.** It has no field to
decode one into, so whatever the certificate API returns on a read, the key in
your state is only ever the one you configured — and on the write-only path there
is none. The provider is *designed* not to look; that is a weaker claim than "the
API never sends it", which we do not make here, and it is the one the provider
can actually keep. If you need to know what crosses the wire on a refresh, assume
the response may carry material and treat `TF_LOG=DEBUG` output accordingly.

Because the provider never stores or adopts the key, `terraform import` cannot
recover it, and the provider says so with a warning rather than producing a
resource that looks complete. **There is no `-refresh-only` escape** — on either attribute. A refresh
writes what the provider's `Read` returns, `Read` has no access to your
configuration, and the key is deliberately preserved from prior state rather than
adopted from the API, so a value you put in the configuration never enters state
by that route. The two honest options for an imported certificate are to accept
the replacement, or to hold it with

```terraform
lifecycle {
  ignore_changes = [private_key_pem, chain_pem]
}
```

until you are ready to take one. On the write-only path the replacement is
unavoidable in any case: `private_key_pem_wo_version` imports as unset, and
setting it is itself a replacement.

Migrating an existing certificate means removing `private_key_pem` and adding the
pair, and that plans a **replacement**, as any change to this resource does. It
does not un-expose the old key: it remains in `terraform.tfstate.backup`, in
prior remote-state versions and in archived plan files. A key that was ever in
state should be treated as disclosed — **issue a new one** rather than
re-uploading the same key through the write-only attribute.

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
fine — a template does not need user data. Omitting the attribute is the only way
to say that, though: `user_data_wo = ""` is refused. Everything
`user_data` documents about the document itself still applies, including writing
it as plain text rather than `base64encode(...)`.

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
template afterwards. What clearing a value actually takes differs by resource —
see the table below.

~> **`<name>_wo = ""` is rejected on every resource that offers a write-only
form** — at plan time when the value is known then, and at apply time when it was
not. The second half matters more than it looks: a value read from another
resource or a data source is unknown at plan, the plan-time validators skip what
they cannot yet see, and that is exactly the sourcing pattern a write-only
attribute exists to serve. The same apply-time check enforces the version
companion and the mutual exclusion. What "clear" means afterwards differs by
resource, and only the first two are awkward:

| Resource | Clearing a write-only value |
|---|---|
| `frostmoln_secret` | No spelling exists. Destroy the secret. |
| `frostmoln_launch_template` | Removing the pair sends nothing and the platform keeps serving the old document. Go back to `user_data = ""`, or destroy the template. |
| `frostmoln_instance` (both attributes) | Removing the pair **does** clear it: the version companion goes to null, that forces a replacement, and the replacement boots with neither value. |
| `frostmoln_appgw_certificate` | Not applicable — a certificate must have a key, so removing the pair is refused outright. Destroy the certificate. |

Migrating an existing template means removing `user_data` and adding the pair.
That plans a normal in-place update, and as with the others it does not un-expose
the old document: it remains in `terraform.tfstate.backup`, in prior remote-state
versions and in archived plan files. Treat anything it embedded as disclosed and
rotate it at its source.

The attributes in the first two tables have no write-only form, and cannot: a
credential the platform mints has to be stored for you to use it, and a
kubeconfig the platform re-issues is fetched rather than supplied. Leaving the
attribute unreferenced, or fetching it out of band, is the only lever there.
