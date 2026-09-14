# Every taggable resource created in the provider's tenant from now on starts
# with these tags — whoever creates it: the portal, the fm CLI, the API or
# Terraform.
resource "frostmoln_tenant_default_tags" "this" {
  tags = {
    environment = "production"
    cost-center = "eu-42"
  }

  # Also add them to the resources the tenant already has, whenever `tags`
  # changes: only the keys a resource is missing, never changing a value it
  # already has. Resources the platform manages are skipped.
  apply_to_existing_on_change = true
}

# The platform copies the defaults onto a resource when it is created, and can
# take up to 30 seconds to see a change, so a resource that must carry them
# depends on this resource: it is then created after the defaults are set —
# and after the apply to existing resources, which stamps only what exists
# when it runs, so without depends_on a resource created alongside may get
# neither. On it they show in tags_all, never in tags.
resource "frostmoln_vpc" "main" {
  name = "main"
  cidr = "10.0.0.0/16"

  depends_on = [frostmoln_tenant_default_tags.this]
}

output "vpc_tags" {
  # { environment = "production", cost-center = "eu-42" } plus any other key on
  # the VPC. A key the VPC's own `tags` (or the provider's default_tags) sets
  # wins over a tenant default of the same key.
  value = frostmoln_vpc.main.tags_all
}
