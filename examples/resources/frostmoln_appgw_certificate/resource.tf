# The private key is written to Terraform state: the platform never returns it,
# so state is the only place it can be kept for a refresh. Use a remote backend
# with encryption at rest, and source the material from a variable or a secret
# store rather than committing PEM to your configuration.

resource "frostmoln_appgw_certificate" "www" {
  gateway_id = frostmoln_application_gateway.edge.id
  name       = "www"

  chain_pem       = var.www_certificate_chain
  private_key_pem = var.www_certificate_key
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
