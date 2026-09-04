# Publishing, ordered after every rule WITHOUT depends_on.
#
# Every rule and exclusion exports a server-bumped `revision`. Feeding them in
# here means any rule change changes an input to this resource, so Terraform's
# own graph runs the publish last and re-runs it exactly when something changed.
# Nothing to remember, and a rule you did not list is visibly not listed.

resource "frostmoln_appgw_waf_policy_publication" "main" {
  gateway_id = frostmoln_application_gateway.edge.id
  policy_id  = frostmoln_appgw_waf_policy.main.id

  rule_revisions = merge(
    {
      for r in [
        frostmoln_appgw_waf_rule.block_scanners,
        frostmoln_appgw_waf_rule.admin_office_only,
        frostmoln_appgw_waf_rule.quiet_942100,
      ] : r.rule_key => r.revision
    },
    {
      for e in [
        frostmoln_appgw_waf_exclusion.search_query,
      ] : e.rule_key => e.revision
    },
  )

  # Refuse to publish anything that would newly break traffic. Raise it
  # deliberately when a rule is SUPPOSED to start blocking.
  max_newly_blocked = 0
}

# What would newly be refused, recorded for review.
output "waf_newly_blocked" {
  value = frostmoln_appgw_waf_policy_publication.main.dry_run_newly_blocked
}

# What the ENFORCED version is actually doing, with "inherit" resolved.
#
# This is the honest answer to "is the firewall blocking right now". A policy's
# own settings are AUTHORED and take effect only once published, and a version's
# mode can be the word "inherit", which answers nothing on its own -- so a check
# written against either can report a gateway as not blocking while requests are
# being refused.
#
# It is null when the mode in force cannot be determined, so read it through
# try().
output "waf_enforced_mode" {
  value = try(frostmoln_appgw_waf_policy_publication.main.effective_mode, "unknown")
}
