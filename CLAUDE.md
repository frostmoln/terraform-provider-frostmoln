# terraform-provider-frostmoln Development Guide

For project-wide conventions and architecture details, see:
- [Shared project context](../claude-config/CLAUDE.md)
- [Architecture & service map](../claude-config/architecture.md)

## Module
`go.frostmoln.internal/terraform-provider-frostmoln`

## Project Overview

Terraform provider for the Frostmoln Cloud Platform. Enables infrastructure-as-code management of compute instances, VPCs, subnets, security groups, public IPs, volumes, buckets, snapshots, SSH keys, S3 credentials, and API keys.

Provider name: `frostmoln`. Resource prefix: `frostmoln_` (e.g., `frostmoln_vpc`, `frostmoln_instance`).

## Architecture

### Technology Stack

```
Go 1.25+                          → Programming language
terraform-plugin-framework         → Provider framework
terraform-plugin-testing           → Acceptance test framework
terraform-plugin-docs              → Documentation generation
```

### Directory Structure

```
terraform-provider-frostmoln/
├── cmd/terraform-provider-frostmoln/
│   └── main.go                    # Entry point
├── internal/
│   ├── provider/
│   │   ├── provider.go            # Schema, Configure, resource/datasource registration
│   │   └── provider_test.go
│   ├── client/
│   │   ├── client.go              # HTTP client, auth, error parsing, tenant path helper
│   │   ├── client_test.go
│   │   ├── poller.go              # WaitForState async polling
│   │   └── poller_test.go
│   ├── resource/
│   │   └── <resource_name>/       # Each resource in its own package
│   │       ├── resource.go        # CRUD + schema
│   │       ├── model.go           # TF model <-> API conversion
│   │       └── resource_test.go
│   └── datasource/
│       └── <datasource_name>/     # Each data source in its own package
│           ├── datasource.go      # Read + schema
│           └── datasource_test.go
├── examples/
│   ├── provider/provider.tf
│   ├── resources/frostmoln_*/resource.tf
│   └── data-sources/frostmoln_*/data-source.tf
├── templates/index.md.tmpl
├── tools/tools.go
├── .gitea/workflows/
│   ├── ci.yml
│   └── pre-commit.yml
├── Makefile
└── go.mod
```

## Common Commands

```bash
# Development
go build ./...            # Build provider binary
go test ./...             # Run unit tests
TF_ACC=1 go test ./...          # Run acceptance tests (requires TF_ACC=1)
golangci-lint run             # Run linter
gofumpt -w .              # Format code

# Installation
go install ./...          # Install provider locally for dev testing

# Documentation
make generate         # Generate provider docs with tfplugindocs

# Maintenance
go mod tidy             # Update dependencies
rm -rf bin/            # Clean build artifacts
```

## Testing

### Running Tests

```bash
# Run all unit tests
go test ./...

# Run specific package tests
go test -v ./internal/client/...
go test -v ./internal/resource/vpc/...

# Run acceptance tests (requires real API)
TF_ACC=1 go test -v -timeout 30m ./internal/...
```

### Writing Tests

Unit tests use `httptest` for HTTP mocking. Each resource test should:
1. Create a mock HTTP server simulating the API
2. Configure the provider client with the mock server URL
3. Test CRUD operations
4. Verify TF state matches expected values

## Code Conventions

### Adding New Resources

1. Create a new directory under `internal/resource/<name>/`
2. Create `model.go` with the TF model struct and API conversion functions
3. Create `resource.go` implementing `resource.Resource` interface (Metadata, Schema, Create, Read, Update, Delete)
4. Create `resource_test.go` with unit tests
5. Register the resource in `internal/provider/provider.go` `Resources()` method
6. Add example HCL in `examples/resources/frostmoln_<name>/resource.tf`
7. For any Optional+Computed attribute that is create-only, follow *Plan modifier
   order* below and add it to `mustReplaceOnRealChange`

### Plan modifier order: `UseStateForUnknown()` before `RequiresReplace()`

On an **Optional+Computed** attribute the two modifiers must be ordered
`UseStateForUnknown()` first, `RequiresReplace()` second:

```go
PlanModifiers: []planmodifier.String{
    stringplanmodifier.UseStateForUnknown(),
    stringplanmodifier.RequiresReplace(),
},
```

The framework marks every Computed attribute whose *config* value is null as
unknown on update, attribute plan modifiers run in slice order chaining their
PlanValue, and `RequiresReplace` is sticky once set. Reversed, it compares that
unknown against the state value, they differ, and it records a replacement — so
for a practitioner who omits the attribute (the intended usage for a
server-defaulted Computed attribute) *any* unrelated change plans a full
destroy/recreate: the VM, the volume and its data.

`UseStateForUnknown()` alone is safe here: it was already running before the fix,
just one modifier too late, so the planned value was pinned to state either way —
the ordering decides only whether a replacement is recorded.

`internal/provider/planmodifier_order_test.go` enforces this behaviourally for
every Optional+Computed attribute of every resource. A new create-only
Optional+Computed attribute goes in its `mustReplaceOnRealChange` table, which
guards the other direction (that `RequiresReplace()` was not simply dropped); the
test fails if an attribute replaces without an entry, so the table cannot drift.

**Exception — `frostmoln_secret`'s `content_type`, `max_versions` and
`recovery_window_days`.** They are create-only but deliberately do NOT replace,
so they are absent from `mustReplaceOnRealChange`. `RequiresReplace()` there
would destroy a live secret and then FAIL to recreate it: a delete is a soft
delete, and `UNIQUE(tenant_id, name)` in the secrets schema has no partial
predicate, so the name stays taken for `recovery_window_days` (up to 30) and the
create 409s. They use `planmod.*UseStateOrDefault` (never a schema `Default`,
which `TransformDefaults` would substitute over an imported secret's real value)
plus a `ModifyPlan` refusal. Copy that pattern for any attribute whose resource
cannot be recreated under the same name.

An attribute with a schema `Default` is exempt: `MarkComputedNilsAsUnknown`
returns a default-bearing attribute untouched, so a null config never makes its
plan value unknown. (An unknown *config expression* still can, but no ordering
helps there — `UseStateForUnknown` bails on an unknown config value by design.)

### Managed-service instance resource conventions

Managed-service offers (databases, caches, web servers, messaging, and the
coming managed Kubernetes) follow one HCL surface — keep new ones consistent:

- **Engine-specific resources, no generic `*_instance`.** Each engine gets its
  own resource (`frostmoln_redis_instance`, `frostmoln_valkey_instance`, …).
  There is no generic `frostmoln_cache_instance`-style umbrella resource (the
  one that existed was removed in #91).
- **Expose `version`, never an engine-prefixed name.** Use the bare `version`
  attribute — not `engine_version` / `mysql_version` / `postgres_version`. The
  backend JSON wire tag stays engine-specific (`engineVersion` / `postgresVersion`);
  the rename is HCL-surface only (the model's `toCreateRequest`/`fromAPI` map
  `version` ↔ the wire tag), so CLI/portals are unaffected (CLAUDE.md #10).
- **Freeform config is `config` (Map of String)**, not `engine_config`, sent as
  the `engineConfig` object on the wire.
- **Flavor is `flavor_id` everywhere.** All managed-service resources expose
  `flavor_id` (the value is a flavor id, e.g. `db.gp1.small`; the wire tag is
  `flavorId`). The db/web resources (mysql/postgres/apache/nginx) were
  normalized from `flavor` to `flavor_id` in a breaking release (Ambix
  019f132b-db61), matching the flagship `frostmoln_instance` and the
  cache/messaging offers. Each bumped its schema `Version` to 1 with a
  `flavor`→`flavor_id` StateUpgrader via the shared `internal/stateupgrade`
  helper, so existing state upgrades cleanly (no spurious diff).

### Adding New Data Sources

1. Create a new directory under `internal/datasource/<name>/`
2. Create `datasource.go` implementing `datasource.DataSource` interface
3. Create `datasource_test.go` with unit tests
4. Register in `internal/provider/provider.go` `DataSources()` method
5. Add example HCL in `examples/data-sources/frostmoln_<name>/data-source.tf`

### API Client

The client in `internal/client/client.go` is self-contained (no dependency on fm CLI or servicekit). Auth is via `X-API-Key` header. Tenant ID is resolved once via `GET /v1/me` and cached.

### Async Resources

Resources that return HTTP 202 (VPCs, volumes) use the poller in `internal/client/poller.go` to wait for completion. The poller is generic and configurable per resource.

## Provider Configuration

```hcl
provider "frostmoln" {
  api_endpoint = "https://api.frostmoln.cloud/api"  # or FROSTMOLN_API_ENDPOINT
  api_key      = var.frostmoln_api_key              # or FROSTMOLN_API_KEY
}
```

**The `/api` suffix is mandatory** — the gateway mounts every customer route
under `/api` (`/api/v1/me`, …), so a bare `https://api.frostmoln.cloud` 404s at
the edge with `NOT_FOUND: the requested resource was not found` while the
provider configures. It is also what the `fm` CLI stores in `~/.fm/config.yaml`,
and the default when `api_endpoint` is unset.

## API Endpoints Reference

| Resource | Base Path | Notes |
|----------|-----------|-------|
| SSH Keys | `/v1/tenants/{t}/sshkeys` | Tenant-scoped |
| Buckets | `/v1/tenants/{t}/buckets` | Name-based ID |
| S3 Credentials | `/v1/tenants/{t}/credentials` | Secret only on create |
| VPCs | `/v1/tenants/{t}/vpcs` | Async create (HTTP 202) |
| Subnets | `/v1/tenants/{t}/subnets` | Most fields ForceNew |
| Security Groups | `/v1/tenants/{t}/security-groups` | Rules are separate resource |
| Security Group Rules | `/v1/tenants/{t}/security-groups/{sg}/rules` | All fields ForceNew |
| Public IPs | `/v1/tenants/{t}/public-ips` | Associate/disassociate actions |
| Volumes | `/v1/tenants/{t}/volumes` | Async create, resize support |
| Snapshots | `/v1/tenants/{t}/snapshots` | Immutable after create |
| Instances | `/v1/tenants/{t}/instances` | Async, resize via actions |
| API Keys | `/v1/api-keys` | Key only on create |
| Images | `/v1/tenants/{t}/images` | Reads public; writes (BYOI custom images) open to every tenant since 2026-08-22 (the `custom-images` entitlement was removed). TENANT-SCOPED — the gateway re-scopes the signed auth context only on a `/tenants/{id}/` path, so a bare `/v1/images` call silently acts on the caller's home tenant. Create → mint upload form → upload to object storage → import → poll, all on the same tenant path |
| Flavors | `/v1/flavors` | Read-only, public |
