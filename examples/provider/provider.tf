terraform {
  required_providers {
    frostmoln = {
      source = "registry.terraform.io/frostmoln/frostmoln"
    }
  }
}

provider "frostmoln" {
  api_endpoint = "https://api.frostmoln.cloud/api"
  api_key      = var.frostmoln_api_key

  # Optional: select the tenant to manage resources in (defaults to your
  # account's default tenant). Targeting another tenant needs an fm CLI / OIDC
  # session — an API key is bound to a single tenant. Also FROSTMOLN_TENANT_ID.
  # tenant_id = "00000000-0000-0000-0000-000000000000"

  # Optional: tags applied to every taggable resource this provider manages. A
  # key a resource sets in its own `tags` wins; each resource's `tags_all`
  # holds the merged set. Changing this block updates every taggable resource.
  default_tags {
    tags = {
      managed-by = "terraform"
    }
  }
}

variable "frostmoln_api_key" {
  description = "API key for the Frostmoln Cloud Platform"
  type        = string
  sensitive   = true
}
