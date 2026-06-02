resource "frostmoln_floating_ip" "example" {
  region      = "sweden"
  instance_id = frostmoln_instance.example.id

  tags = {
    service = "web"
  }
}
