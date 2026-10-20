data "frostmoln_instance" "web" {
  id = "inst-abc123"
}

output "instance_ip" {
  value = data.frostmoln_instance.web.private_ip
}

# The instance's internal DNS name; resolves from inside its VPC only.
output "instance_fqdn" {
  value = data.frostmoln_instance.web.fqdn
}
