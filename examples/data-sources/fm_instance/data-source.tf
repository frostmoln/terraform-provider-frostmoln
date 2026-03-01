data "fm_instance" "web" {
  id = "inst-abc123"
}

output "instance_ip" {
  value = data.fm_instance.web.private_ip
}
