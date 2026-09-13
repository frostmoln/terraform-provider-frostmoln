# Colour every `env=prod` tag red and every `env=staging` tag amber, in every
# tenant of the organization that owns the provider's tenant.
resource "frostmoln_tag_color" "prod" {
  key   = "env"
  value = "prod"
  color = "#d73a49"
}

resource "frostmoln_tag_color" "staging" {
  key   = "env"
  value = "staging"
  color = "#dbab09"
}

# Without `value` the rule matches ANY value of the key — here, every `team`
# tag is blue. A rule for an exact value (above) wins over the key-only rule for
# the same key.
resource "frostmoln_tag_color" "team" {
  key   = "team"
  color = "#1f6feb"
}

# `value = ""` is a different rule: it matches only a tag whose value is empty.
resource "frostmoln_tag_color" "unowned" {
  key   = "owner"
  value = ""
  color = "#6a737d"
}
