# The drift-detection recipe for VPC routes.
#
# `frostmoln_vpc_route` is one resource per route and deliberately
# non-authoritative — no resource owns the table — so a route added out of band
# (portal, `fm`, API, a colleague) is invisible to every plan. THE SHADOW CASE
# this exists for: Terraform owns 0.0.0.0/0 -> appliance, and someone adds
# 10.20.0.0/16 -> other-box. Longest prefix wins, the plan stays clean, and
# traffic goes where the configuration does not say. This data source reads the
# whole tenant-visible table; the check block below pins it and turns the
# difference into a failed plan.

data "frostmoln_vpc_routes" "table" {
  vpc_id = frostmoln_vpc.main.id
}

# Destinations this configuration manages. One route here; with several, give
# the resource a for_each and use destinations = toset(values(...)[*].destination).
locals {
  managed_destinations = [
    frostmoln_vpc_route.appliance_default.destination,
  ]
}

# Every tenant-visible row must be a route this configuration declares.
# (Platform-owned routes never appear in the listing, by design, so nothing
# needs filtering for the pin to be total over what the tenant controls.)
check "vpc_routes_pinned" {
  data = data.frostmoln_vpc_routes.table

  assert {
    condition = length([
      for row in data.frostmoln_vpc_routes.table.routes : row.destination
      if !contains(local.managed_destinations, row.destination)
    ]) == 0
    error_message = (
      "This VPC has routes Terraform does not manage. They were added outside Terraform — " +
      "in the portal, with `fm`, or by someone else — and one of them may be shadowing a " +
      "managed route (a more-specific destination wins the traffic). Import the route with " +
      "`terraform import frostmoln_vpc_route.<name> \"${frostmoln_vpc.main.id}/<destination>\"` " +
      "or remove it, then re-run the plan."
    )
  }
}

resource "frostmoln_vpc_route" "appliance_default" {
  vpc_id      = frostmoln_vpc.main.id
  destination = "0.0.0.0/0"
  next_hop    = frostmoln_instance.appliance.private_ip
}
