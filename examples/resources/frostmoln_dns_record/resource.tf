resource "frostmoln_dns_zone" "example" {
  name  = "example.com."
  email = "admin@example.com"
}

# An A record with two values.
resource "frostmoln_dns_record" "www" {
  zone_id = frostmoln_dns_zone.example.id
  name    = "www"
  type    = "A"
  records = ["203.0.113.10", "203.0.113.11"]
  ttl     = 300
}

# An MX record at the zone apex ("@"); the priority is part of the value.
resource "frostmoln_dns_record" "mx" {
  zone_id = frostmoln_dns_zone.example.id
  name    = "@"
  type    = "MX"
  records = ["10 mail.example.com."]
}
