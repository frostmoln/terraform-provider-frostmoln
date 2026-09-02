// Package docs holds documentation strings shared by more than one resource
// schema, so wording and URLs are one edit rather than nine copies drifting
// apart. Same idea as public_ip.MutualExclusivityNote, one level up.
package docs

// StateSecretsGuide is the published location of the "Secrets in Terraform
// state" guide (source: templates/guides/state-and-secrets.md.tmpl).
//
// An absolute URL rather than a relative docs path on purpose: these strings
// also surface in `terraform providers schema -json` and in terraform-ls hover
// cards, where a relative link resolves to nothing. `latest` on purpose too — a
// reader on an old provider version still lands on current advice.
const StateSecretsGuide = "https://registry.terraform.io/providers/frostmoln/frostmoln/latest/docs/guides/state-and-secrets" // pragma: allowlist secret

// StateSecretNote is the sentence carried by every attribute whose value is
// written to Terraform state in plaintext. Terraform persists every attribute
// of every managed resource; `Sensitive: true` only redacts CLI output.
const StateSecretNote = "Stored in Terraform state in plaintext — `sensitive` redacts CLI output, not the state file. " + // pragma: allowlist secret
	"See the [Secrets in Terraform state](" + StateSecretsGuide + ") guide."

// UserDataStateNote is the user_data variant: the risk there is not the
// document itself but whatever a practitioner embeds in it.
const UserDataStateNote = "The document is written to Terraform state in plaintext, so anything embedded in it — " +
	"credentials, tokens, private keys — is readable by anyone who can read the state; `sensitive` redacts CLI " +
	"output, not the state file. See the [Secrets in Terraform state](" + StateSecretsGuide + ") guide."
