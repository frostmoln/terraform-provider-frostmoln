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
}

variable "frostmoln_api_key" {
  description = "API key for the Frostmoln Cloud Platform"
  type        = string
  sensitive   = true
}
