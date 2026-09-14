# Every taggable resource created in the provider's tenant from now on starts
# with these tags — whoever creates it: the portal, the fm CLI, the API or
# Terraform. Existing resources are not changed.
resource "frostmoln_tenant_default_tags" "this" {
  tags = {
    environment = "production"
    cost-center = "eu-42"
  }
}

# The platform copies the defaults onto a resource when it is created, and can
# take up to 30 seconds to see a change, so a resource that must carry them
# depends on this resource. On it they show in tags_all, never in tags.
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
