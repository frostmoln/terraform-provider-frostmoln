resource "frostmoln_dns_zone" "example" {
  name  = "example.com."
  email = "admin@example.com"
  ttl   = 3600
}

# The zone is assigned a delegation nameserver set. Delegate your domain at your
# registrar to exactly these name servers.
output "delegation_name_servers" {
  value = frostmoln_dns_zone.example.name_servers
}
