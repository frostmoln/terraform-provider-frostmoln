resource "frostmoln_nginx_instance" "site" {
  name       = "site-1"
  version    = "1.30"
  flavor_id  = "web.gp1.small"
  storage_gb = 20
  vpc_id     = frostmoln_vpc.main.id
  subnet_id  = frostmoln_subnet.public.id

  php_enabled = true
  php_version = "8.3"

  # Curated allowlist keys — anything else is rejected by the API.
  config = {
    indexFiles        = "index.html index.htm"
    spaFallback       = "true"
    clientMaxBodySize = "25m"
    gzip              = "true"
    securityHeaders   = "true"
  }

  # Allocate and attach a public IP so the site is reachable from the internet.
  # A public IP is billed for as long as it is held.
  public = true
}

output "nginx_site_public_ip" {
  value = frostmoln_nginx_instance.site.public_ip
}
