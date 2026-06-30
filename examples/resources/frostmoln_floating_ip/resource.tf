resource "frostmoln_floating_ip" "example" {
  instance_id = frostmoln_instance.example.id

  tags = {
    service = "web"
  }
}
