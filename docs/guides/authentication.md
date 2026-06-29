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

The `fm` CLI stores its API endpoint **with the `/api` suffix**
(`https://api.frostmoln.cloud/api`) — the gateway mounts customer routes under
`/api`. When the credential comes from the CLI config and you have **not** set
`api_endpoint` explicitly, the provider adopts the context's endpoint (falling
back to `https://api.frostmoln.cloud/api` if the config omits one), so both API
calls and the OIDC discovery resolve correctly. If you set `api_endpoint`
yourself while using a CLI session, include the `/api` suffix — and note that an
OIDC bearer session requires an `https` endpoint.

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

## Precedence summary

| Source | How | Refresh |
|--------|-----|---------|
| `api_key` attribute | `X-API-Key` | n/a |
| `FROSTMOLN_API_KEY` | `X-API-Key` | n/a |
| fm CLI `credentials.api_key` | `X-API-Key` | n/a |
| fm CLI `credentials.access_token` | `Authorization: Bearer` | automatic, written back |
