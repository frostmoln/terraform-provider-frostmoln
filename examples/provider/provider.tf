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

variable "frostmoln_api_key" {
  description = "API key for the Frostmoln Cloud Platform"
  type        = string
  sensitive   = true
}
