data "frostmoln_dns_zone" "example" {
  name = "example.com."
}

# Read the zone's delegation name servers to configure your registrar.
output "delegation_name_servers" {
  value = data.frostmoln_dns_zone.example.name_servers
}
