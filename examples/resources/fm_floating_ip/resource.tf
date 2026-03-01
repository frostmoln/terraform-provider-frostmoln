resource "fm_floating_ip" "example" {
  region      = "eu-north-1"
  instance_id = fm_instance.example.id

  tags = {
    service = "web"
  }
}
