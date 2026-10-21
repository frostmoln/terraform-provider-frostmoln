resource "frostmoln_nginx_instance" "site" {
  name       = "site-1"
  version    = "1.30"
  flavor_id  = "web.co1.small"
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

  # TLS: once TLS for managed webservers is available, every new instance
  # serves HTTPS on 443 with a certificate for its internal hostname
  # (tls_enabled is read-only: do not set it). require_tls = true makes port 80
  # only redirect to https; omitted (false) the site is served on 80 too.
  # Fixed at create; changing it replaces the instance. Public HTTPS (a public
  # name) is the frostmoln_application_gateway's.
  # require_tls = true

  # Resource tags: your own metadata for search and filtering. Merged with
  # the provider's default_tags; changing them updates in place.
  tags = {
    env = "prod"
  }
}

output "nginx_site_public_ip" {
  value = frostmoln_nginx_instance.site.public_ip
}

# The internal HTTPS endpoint (inside the VPC), verified against the bundle
# from the frostmoln_ca_bundle data source; null on an instance without TLS.
output "nginx_site_internal_https" {
  value = frostmoln_nginx_instance.site.tls_enabled ? "https://${frostmoln_nginx_instance.site.hostname}/" : null
}
