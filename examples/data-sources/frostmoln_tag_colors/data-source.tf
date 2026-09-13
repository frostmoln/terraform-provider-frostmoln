# The colour rules of the organization that owns the provider's tenant —
# including rules created in the portal, with the ids to import them by.
data "frostmoln_tag_colors" "current" {}

output "tag_colour_organization" {
  value = data.frostmoln_tag_colors.current.organization_id
}

# key=value (or key=* for a key-only rule) -> colour.
output "tag_colours" {
  value = {
    for rule in data.frostmoln_tag_colors.current.rules :
    "${rule.key}=${rule.value == null ? "*" : rule.value}" => rule.color
  }
}
