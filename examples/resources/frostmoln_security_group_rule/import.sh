# Import a security group rule by {security_group_id}/{rule_id}.
terraform import frostmoln_security_group_rule.web_https "sg-abc123/sgr-def456"

# This is how a rule Terraform does not already own is brought under management —
# one added through the portal, the fm CLI or the API, or one of the two allow-all
# egress rules (IPv4 + IPv6) that exist on every security group from the moment it
# is created. Terraform reports no drift for such a rule and removes none of them,
# so list the group outside Terraform (fm, the portal or the API return its full
# rule set) to find the rule ids to import.
