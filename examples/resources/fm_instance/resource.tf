resource "fm_instance" "example" {
  name      = "web-server-01"
  flavor_id = data.fm_flavor.medium.id
  image_id  = data.fm_image.ubuntu.id
  region    = "eu-north-1"
  zone      = "eu-north-1a"
  vpc_id    = fm_vpc.example.id
  subnet_id = fm_subnet.example.id

  security_groups = [fm_security_group.web.id]
  ssh_key_names   = [fm_ssh_key.example.name]

  tags = {
    role        = "web"
    environment = "production"
  }
}
