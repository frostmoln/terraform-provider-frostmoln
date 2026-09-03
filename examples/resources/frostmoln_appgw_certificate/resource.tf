# The private key is written to Terraform state: the provider never adopts one
# from an API response, so state is the only place it is kept across refreshes. Use a remote backend
# with encryption at rest, and source the material from a variable or a secret
# store rather than committing PEM to your configuration. Prefer
# private_key_pem_wo (the second block below), which keeps the key out of state
# entirely.

resource "frostmoln_appgw_certificate" "www" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "www"

  chain_pem       = var.www_certificate_chain
  private_key_pem = var.www_certificate_key
}

# The write-only form. private_key_pem_wo never reaches the plan or state; it
# needs Terraform 1.11 or later. Exactly one of private_key_pem and
# private_key_pem_wo must be set.
#
# Because Terraform cannot see a write-only value change, bumping
# private_key_pem_wo_version is what uploads the current key — and it does so by
# REPLACING the certificate, because the certificate API has no update
# operation. Attach it with create_before_destroy if the listener cannot take
# the interruption.
resource "frostmoln_appgw_certificate" "api" {
  gateway_id = frostmoln_application_gateway.edge.id

  # The name carries the version, so create_before_destroy has a distinct name
  # to create under. With a fixed name the replacement is created while the old
  # certificate still exists — and if the gateway treats (gateway, name) as an
  # identity, that collides and the rotation fails at the worst moment.
  name = "api-${var.api_certificate_version}"

  chain_pem = var.api_certificate_chain

  private_key_pem_wo         = var.api_certificate_key
  private_key_pem_wo_version = var.api_certificate_version

  lifecycle {
    create_before_destroy = true
  }
}

variable "www_certificate_chain" {
  type        = string
  description = "PEM chain, leaf first."
}

variable "www_certificate_key" {
  type        = string
  sensitive   = true
  description = "PEM private key for the chain."
}

variable "api_certificate_chain" {
  type        = string
  description = "PEM chain, leaf first."
}

variable "api_certificate_key" {
  type        = string
  sensitive   = true
  description = "PEM private key for the chain. Never reaches Terraform state."
}

variable "api_certificate_version" {
  type        = string
  default     = "1"
  description = "Bump to rotate. Do NOT derive it from the key — it is not sensitive and is printed in plan output."
}
