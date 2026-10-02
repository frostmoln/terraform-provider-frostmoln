data "frostmoln_tenant_quotas" "this" {}

# vCPUs still available in the tenant. used is null when usage is unknown.
output "vcpu_available" {
  value = data.frostmoln_tenant_quotas.this.quotas["vcpu"].used == null ? null : (
    data.frostmoln_tenant_quotas.this.quotas["vcpu"].limit - data.frostmoln_tenant_quotas.this.quotas["vcpu"].used
  )
}
