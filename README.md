# Terraform Provider for Frostmoln Cloud

Terraform provider for the [Frostmoln Cloud Platform](https://nordiclight.cloud), enabling infrastructure-as-code management of cloud resources with full data sovereignty in EU/EEA datacenters.

## Requirements

- [Terraform](https://www.terraform.io/downloads.html) >= 1.0
- [Go](https://golang.org/doc/install) >= 1.25 (to build the provider plugin)

## Building The Provider

```sh
git clone go.frostmoln.internal/terraform-provider-frostmoln
cd terraform-provider-frostmoln
make build
```

## Using The Provider

```hcl
terraform {
  required_providers {
    frostmoln = {
      source = "registry.terraform.io/nordiclight/frostmoln"
    }
  }
}

provider "frostmoln" {
  api_endpoint = "https://api.nordiclight.cloud"
  api_key      = var.frostmoln_api_key
}
```

## Developing The Provider

See [CLAUDE.md](CLAUDE.md) for development guidelines.

```sh
make build     # Build
make test      # Unit tests
make testacc   # Acceptance tests
make install   # Install locally
```
