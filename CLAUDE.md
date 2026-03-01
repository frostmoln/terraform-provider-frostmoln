# terraform-provider-frostmoln Development Guide

For project-wide conventions and architecture details, see:
- [Shared project context](../claude-config/CLAUDE.md)
- [Architecture & service map](../claude-config/architecture.md)

## Module
`git.nl.cloud/NordicLight/terraform-provider-frostmoln`

## Project Overview

Terraform provider for the NordicLight (Frostmoln) Cloud Platform. Enables infrastructure-as-code management of compute instances, VPCs, subnets, security groups, floating IPs, volumes, buckets, snapshots, SSH keys, S3 credentials, and API keys.

Provider name: `frostmoln`. Resource prefix: `fm_`.

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
│   ├── resources/fm_*/resource.tf
│   └── data-sources/fm_*/data-source.tf
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
make build            # Build provider binary
make test             # Run unit tests
make testacc          # Run acceptance tests (requires TF_ACC=1)
make lint             # Run linter
make fmt              # Format code

# Installation
make install          # Install provider locally for dev testing

# Documentation
make generate         # Generate provider docs with tfplugindocs

# Maintenance
make deps             # Update dependencies
make clean            # Clean build artifacts
```

## Testing

### Running Tests

```bash
# Run all unit tests
make test

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
6. Add example HCL in `examples/resources/fm_<name>/resource.tf`

### Adding New Data Sources

1. Create a new directory under `internal/datasource/<name>/`
2. Create `datasource.go` implementing `datasource.DataSource` interface
3. Create `datasource_test.go` with unit tests
4. Register in `internal/provider/provider.go` `DataSources()` method
5. Add example HCL in `examples/data-sources/fm_<name>/data-source.tf`

### API Client

The client in `internal/client/client.go` is self-contained (no dependency on nlctl or servicekit). Auth is via `X-API-Key` header. Tenant ID is resolved once via `GET /v1/me` and cached.

### Async Resources

Resources that return HTTP 202 (VPCs, volumes) use the poller in `internal/client/poller.go` to wait for completion. The poller is generic and configurable per resource.

## Provider Configuration

```hcl
provider "frostmoln" {
  api_endpoint = "https://api.nordiclight.cloud"  # or FROSTMOLN_API_ENDPOINT
  api_key      = var.frostmoln_api_key             # or FROSTMOLN_API_KEY
}
```

## API Endpoints Reference

| Resource | Base Path | Notes |
|----------|-----------|-------|
| SSH Keys | `/v1/users/{user_id}/sshkeys` | User-scoped |
| Buckets | `/v1/tenants/{t}/buckets` | Name-based ID |
| S3 Credentials | `/v1/tenants/{t}/credentials` | Secret only on create |
| VPCs | `/v1/tenants/{t}/vpcs` | Async create (HTTP 202) |
| Subnets | `/v1/tenants/{t}/subnets` | Most fields ForceNew |
| Security Groups | `/v1/tenants/{t}/security-groups` | Rules are separate resource |
| Security Group Rules | `/v1/tenants/{t}/security-groups/{sg}/rules` | All fields ForceNew |
| Floating IPs | `/v1/tenants/{t}/floating-ips` | Associate/disassociate actions |
| Volumes | `/v1/tenants/{t}/volumes` | Async create, resize support |
| Snapshots | `/v1/tenants/{t}/snapshots` | Immutable after create |
| Instances | `/v1/tenants/{t}/instances` | Async, resize via actions |
| API Keys | `/v1/api-keys` | Key only on create |
| Images | `/v1/images` | Read-only, public |
| Flavors | `/v1/flavors` | Read-only, public |
