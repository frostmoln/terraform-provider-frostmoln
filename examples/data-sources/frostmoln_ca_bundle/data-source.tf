data "frostmoln_ca_bundle" "this" {
  # Pin the bundle to the SHA-256 fingerprints published in the Frostmoln
  # documentation; the read fails unless the bundle holds exactly these.
  #
  # Root rollover: the next root is first ADDED to the bundle, and its
  # fingerprint is published on the documentation page in advance. Until this
  # set is updated to the newly published one (old and next root), the read
  # fails. When the old root is later removed, update the set again.
  expected_fingerprints = var.frostmoln_ca_fingerprints
}

variable "frostmoln_ca_fingerprints" {
  description = "SHA-256 fingerprints of the Frostmoln customer CA, from the documentation."
  type        = set(string)
}

# The PEM to install as a trust anchor, e.g. written to a file and used as
# psql "sslmode=verify-full sslrootcert=frostmoln-ca.pem ...".
output "ca_bundle_pem" {
  value = data.frostmoln_ca_bundle.this.pem
}

output "ca_fingerprints" {
  value = data.frostmoln_ca_bundle.this.fingerprints
}

output "ca_certificates" {
  value = [for c in data.frostmoln_ca_bundle.this.certificates : "${c.subject} (until ${c.not_after})"]
}
