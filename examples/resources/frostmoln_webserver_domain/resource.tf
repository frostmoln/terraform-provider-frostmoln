# Bind domains to a managed webserver instance. Point each domain's DNS at the
# instance's public IP, and mark one binding as the default. The resource is
# create/delete only, so changing any argument replaces the binding.
resource "frostmoln_webserver_domain" "apex" {
  instance_id = frostmoln_nginx_instance.site.id
  domain_name = "example.com"
  is_default  = true
}

resource "frostmoln_webserver_domain" "www" {
  instance_id = frostmoln_nginx_instance.site.id
  domain_name = "www.example.com"
}
