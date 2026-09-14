# The default tags of the provider's tenant — the tags the platform copies onto
# every taggable resource created in it.
data "frostmoln_tenant_default_tags" "current" {}

output "tenant_default_tags" {
  value = data.frostmoln_tenant_default_tags.current.tags
}
