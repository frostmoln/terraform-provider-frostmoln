# Resolve a security group that Terraform did not create by NAME — including
# the platform-managed group a managed database, cache, webserver or messaging
# instance attaches. This resolver's job is the id.
data "frostmoln_security_group" "web" {
  name = "web"
}

# The rules live with their own surface (one collection, one owner):
# frostmoln_security_group_rules reads the whole stored rule set of the group
# this lookup resolves.
data "frostmoln_security_group_rules" "web" {
  security_group_id = data.frostmoln_security_group.web.id
}

# A lookup by id behaves identically for groups whose UUID is already known.
data "frostmoln_security_group" "by_id" {
  id = data.frostmoln_security_group.web.id
}

# The optional vpc_id narrows a name lookup (sent as the vpcId filter) and is
# echoed back from the read.
data "frostmoln_security_group" "in_vpc" {
  name   = "web"
  vpc_id = "vpc-1d2e3f4a-0000-0000-0000-000000000000"
}

# A name that matches nothing — or more than one security group — fails the
# read loudly rather than picking a group for you.
