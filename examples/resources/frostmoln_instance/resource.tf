resource "frostmoln_instance" "example" {
  name      = "web-server-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  region    = "eu-north-1"
  zone      = "eu-north-1a"
  vpc_id    = frostmoln_vpc.example.id
  subnet_id = frostmoln_subnet.example.id

  security_groups = [frostmoln_security_group.web.id]
  ssh_key_names   = [frostmoln_ssh_key.example.name]

  tags = {
    role        = "web"
    environment = "production"
  }
}
