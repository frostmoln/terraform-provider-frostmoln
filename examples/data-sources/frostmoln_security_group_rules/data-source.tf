# The drift-detection recipe for security group rules.
#
# `frostmoln_security_group_rule` is one resource per rule and the parent
# `frostmoln_security_group` manages the group and nothing else, so a rule added
# out of band (portal, `fm`, API, a colleague) is invisible to every plan. Rules
# are additive — traffic matching ANY rule is allowed — so every unplanned row
# makes the group more permissive than the configuration says; ONE rule open to
# the world is as dangerous as several. This data source reads the group's
# whole stored set (the group read embeds its rules; rules have no GET of their
# own), and the check block below pins it by id — rules carry server ids, so
# the pin is id-based, not signature-based.

data "frostmoln_security_group_rules" "app" {
  security_group_id = frostmoln_security_group.app.id
}

# Rule ids this configuration manages. One rule here; with several, give the
# resource a for_each and use ids = toset(values(...)[*].id).
locals {
  managed_rule_ids = [
    frostmoln_security_group_rule.ssh_ingress.id,
  ]
}

# EVERY row on the group must be a rule this configuration declares — except
# the platform-injected allow-all egress pair, whose exact signature is
# egress + any + BOTH remotes null. The filter bets on a platform invariant:
# the API refuses to CREATE a rule with an empty remote, so nothing else can
# ever match that signature — if that refusal ever loosens, tighten the filter.
# An INGRESS row with both remotes null is the opposite of benign — it allows
# traffic from ANY source (fail-open) — and MUST NOT be filtered: the check
# below rightly fails on one until it is deleted. On new groups the cleaner
# answer is delete_default_egress = true on the frostmoln_security_group
# resource, which removes the pair at create.
check "security_group_rules_pinned" {
  data = data.frostmoln_security_group_rules.app

  assert {
    condition = length([
      for row in data.frostmoln_security_group_rules.app.rules : row.id
      if !contains(local.managed_rule_ids, row.id) && !(
        row.direction == "egress" &&
        row.protocol == "any" &&
        row.remote_cidr == null &&
        row.remote_group_id == null
      )
    ]) == 0
    error_message = (
      "This security group has rules Terraform does not manage. They were added outside Terraform — " +
      "in the portal, with `fm`, or by someone else — and rules are additive, so the group now allows " +
      "more than the configuration says. If any row has ingress and neither remote set, it allows " +
      "traffic from ANY source; close it first. Import the rule with " +
      "`terraform import frostmoln_security_group_rule.<name> \"${frostmoln_security_group.app.id}/<rule_id>\"` " +
      "or remove it, then re-run the plan."
    )
  }
}

resource "frostmoln_security_group" "app" {
  name                  = "app"
  delete_default_egress = true
}

resource "frostmoln_security_group_rule" "ssh_ingress" {
  security_group_id = frostmoln_security_group.app.id
  direction         = "ingress"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_cidr       = "203.0.113.0/24"
  description       = "ssh from the office"
}

# The injected egress pair, if it still exists on a group you did not create
# with delete_default_egress = true: import it ONLY IN ORDER TO DESTROY it, and
# remove the block once the destroy has run — leaving it re-creates a rule the
# platform refuses (no rule may be created with an empty remote).
# terraform import frostmoln_security_group_rule.old_egress \
#   "${frostmoln_security_group.app.id}/<injected_rule_id>"
