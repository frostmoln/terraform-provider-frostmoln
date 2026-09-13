# A tag colour rule is imported by <organization_id>/<rule_id>. The
# frostmoln_tag_colors data source lists the rules with their ids.
terraform import frostmoln_tag_color.prod <organization-id>/<rule-id>

# Or by the rule id alone, for a rule of the organization that owns the
# provider's tenant.
terraform import frostmoln_tag_color.prod <rule-id>
