# Import a VPC route by {vpc_id}/{destination}.
terraform import frostmoln_vpc_route.partner "vpc-abc123/203.0.113.0/24"

# The destination is a CIDR and contains a slash of its own; only the FIRST
# slash separates the VPC id from the destination.
#
# This is how a route Terraform does not already own is brought under
# management — one added through the portal, the fm CLI or the API. Platform-owned
# routes cannot be imported: they are not part of the tenant's route set, a read
# does not list them, and a delete for one answers ROUTE_NOT_FOUND.
