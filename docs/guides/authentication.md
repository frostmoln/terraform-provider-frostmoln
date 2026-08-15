---
page_title: "Authentication"
subcategory: "Guides"
description: |-
  How the Frostmoln Terraform provider authenticates: API key, environment
  variable, or an existing fm CLI session with automatic OIDC token refresh.
---

# Authentication

The Frostmoln provider supports three credential sources. They are tried in
order and the **first one found wins**:

1. The `api_key` provider attribute.
2. The `FROSTMOLN_API_KEY` environment variable.
3. An existing **`fm` CLI session** in `~/.fm/config.yaml`.

## 1. API key attribute

The most explicit form. Pass an API key created with `fm auth api-key` or in the
portal. Keep it out of source control — read it from a variable:

```terraform
provider "frostmoln" {
  api_key = var.frostmoln_api_key
}

variable "frostmoln_api_key" {
  type      = string
  sensitive = true
}
```

## 2. Environment variable

Set `FROSTMOLN_API_KEY` (and optionally `FROSTMOLN_API_ENDPOINT`) and leave the
attribute unset:

```terraform
provider "frostmoln" {}
```

```console
$ export FROSTMOLN_API_KEY=fmk_prod_xxxxx
$ terraform plan
```

## 3. fm CLI session (default fallback)

When no API key is configured, the provider falls back to the credentials the
`fm` CLI stores in `~/.fm/config.yaml`. After you have logged in once:

```console
$ fm auth login
$ terraform plan   # uses your fm session — no api_key needed
```

This mirrors how `kubectl`, `aws`, and `gcloud` reuse a CLI login.

The provider uses whichever credential the active CLI context holds:

- **Stored API key** (`credentials.api_key`) — sent as is.
- **OIDC session** (`credentials.access_token` + `refresh_token`) — sent as a
  Bearer token. Access tokens are short-lived (~30 minutes), so the provider
  **refreshes the token automatically** — proactively when it is near expiry and
  reactively on a `401` — then **writes the rotated token pair back** to
  `~/.fm/config.yaml`. Your `fm` session keeps working afterwards.

The provider never logs tokens. If the config file is group/other-readable it
emits a warning (it holds credentials); run `chmod 600 ~/.fm/config.yaml`.

### Choosing a context or file

```terraform
provider "frostmoln" {
  cli_context     = "staging"             # default: the file's current_context
  cli_config_path = "/path/to/config.yaml" # default: ~/.fm/config.yaml
}
```

`cli_config_path` can also be set via `FROSTMOLN_CLI_CONFIG`.

### Endpoint handling

The gateway mounts customer routes under `/api`, so **every** endpoint the
provider talks to carries that suffix — the default
(`https://api.frostmoln.cloud/api`) and any `api_endpoint` you set yourself. An
endpoint without it 404s at the edge (`NOT_FOUND: the requested resource was not
found`) while the provider configures.

The `fm` CLI stores its endpoint in the same `/api` form. When the credential
comes from the CLI config and you have **not** set `api_endpoint` explicitly,
the provider adopts the context's endpoint (falling back to the default if the
config omits one), so both API calls and the OIDC discovery resolve correctly.
Note that an OIDC bearer session requires an `https` endpoint.

## Disabling the CLI fallback (CI)

In CI you usually want to require an explicit key and never read a developer's
home directory. Disable the fallback:

```terraform
provider "frostmoln" {
  use_cli_config = false   # or FROSTMOLN_USE_CLI_CONFIG=false
}
```

With the fallback off and no `api_key` / `FROSTMOLN_API_KEY` present, the
provider fails with a clear "missing credentials" error.

## Which credential am I actually using?

The sources above are tried in order and the first one that has a value wins,
**silently**. That matters most in the case that looks like success: if your
shell has no `FROSTMOLN_API_KEY` and you have run `fm auth login`, Terraform
authenticates with your **CLI session** and creates every resource as *you* —
your API key is never used, and it will correctly show as never used in the
portal.

Ask the provider:

```console
$ TF_LOG_PROVIDER=INFO terraform plan 2>&1 | grep -E "credentials resolved|authenticated"
... Frostmoln provider credentials resolved: credential_kind=cli_session
    credential_source="the fm CLI login session (/home/you/.fm/config.yaml, context default), NOT an API key"
... Frostmoln provider authenticated: credential_kind=cli_session user_id=... tenant_id=...
```

`credential_kind` is one of `api_key_attribute`, `api_key_env`, `cli_api_key`
or `cli_session`. If it says `cli_session` and you meant to use an API key,
either export `FROSTMOLN_API_KEY`, set the `api_key` attribute, or set
`use_cli_config = false` to make the fallback an error instead of a surprise.
The same source is named in the `Failed to Configure Provider` diagnostic, so a
failed run tells you which credential was rejected without any extra logging.

Note that `api_key = ""` counts as unset: an empty attribute falls through to
`FROSTMOLN_API_KEY` rather than disabling it.

## Precedence summary

| Source | How | Refresh |
|--------|-----|---------|
| `api_key` attribute | `X-API-Key` | n/a |
| `FROSTMOLN_API_KEY` | `X-API-Key` | n/a |
| fm CLI `credentials.api_key` | `X-API-Key` | n/a |
| fm CLI `credentials.access_token` | `Authorization: Bearer` | automatic, written back |
