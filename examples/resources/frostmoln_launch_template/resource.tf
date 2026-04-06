resource "frostmoln_launch_template" "web" {
  name      = "web-server-template"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.example.id

  ssh_key_ids        = [frostmoln_ssh_key.deploy.id]
  security_group_ids = [frostmoln_security_group.web.id]

  metadata = {
    role = "web"
  }

  tags = {
    environment = "production"
  }
}
