resource "frostmoln_floating_ip" "example" {
  region      = "eu-north-1"
  instance_id = frostmoln_instance.example.id

  tags = {
    service = "web"
  }
}
