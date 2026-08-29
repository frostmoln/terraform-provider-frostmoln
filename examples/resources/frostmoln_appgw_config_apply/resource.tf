# Authoring is not applying. A listener, route, backend, certificate or
# published WAF version is RECORDED when you apply it — the gateway keeps
# serving what it last acknowledged until a configuration apply dispatches the
# change. Without this resource, `terraform apply` records your intent and the
# gateway does not serve it.
#
# `triggers` is what orders this. Feed in a value from each child that moves
# when the child moves; any change to any of them changes an input here, so
# Terraform's own graph runs this last and re-runs it exactly when something
# changed. A child left out is a child whose change will not be dispatched.
resource "frostmoln_appgw_config_apply" "edge" {
  gateway_id = frostmoln_application_gateway.edge.id

  triggers = {
    # Listeners, routes, pools, backends and certificates carry `updated_at`, a
    # server-set timestamp with sub-second precision.
    "listener.https"  = frostmoln_appgw_listener.https.updated_at
    "route.app"       = frostmoln_appgw_route.app.updated_at
    "pool.app"        = frostmoln_appgw_backend_pool.app.updated_at
    "backend.app_1"   = frostmoln_appgw_backend.app_1.updated_at
    "certificate.app" = frostmoln_appgw_certificate.app.updated_at

    # A published WAF version also reaches the appliance only on the next
    # configuration apply, so the publication belongs here too. It exports a
    # NUMBER, so stringify it.
    #
    # NOTE: the appliance sources its ruleset from the gateway's
    # applied_waf_version, which the platform does not yet write — so a
    # publication is dispatched but no compiled ruleset is packed with it today.
    # Listing it here is still correct and becomes effective when that lands.
    "waf.publication" = tostring(frostmoln_appgw_waf_policy_publication.app.version)
  }
}

# The apply fails if the appliance refuses the configuration, quoting the
# proxy's own words — so a `terraform apply` that succeeds means the gateway is
# serving what you declared.
output "gateway_config_revision" {
  value = frostmoln_appgw_config_apply.edge.revision
}
