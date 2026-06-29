terraform {
  required_providers {
    frostmoln = {
      source = "registry.terraform.io/frostmoln/frostmoln"
    }
  }
}

provider "frostmoln" {
  api_endpoint = "https://api.frostmoln.cloud"
  api_key      = var.frostmoln_api_key

  # Optional: select the tenant to manage resources in (defaults to your
  # account's default tenant). Targeting another tenant needs an fm CLI / OIDC
  # session — an API key is bound to a single tenant. Also FROSTMOLN_TENANT_ID.
  # tenant_id = "00000000-0000-0000-0000-000000000000"
}

variable "frostmoln_api_key" {
  description = "API key for the Frostmoln Cloud Platform"
  type        = string
  sensitive   = true
}
