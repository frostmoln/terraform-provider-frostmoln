data "frostmoln_instance" "web" {
  id = "inst-abc123"
}

output "instance_ip" {
  value = data.frostmoln_instance.web.private_ip
}
