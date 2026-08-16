resource "frostmoln_launch_template" "web" {
  name      = "web-server-template"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.example.id

  ssh_key_ids        = [frostmoln_ssh_key.deploy.id]
  security_group_ids = [frostmoln_security_group.web.id]

  # Cloud-init as PLAIN TEXT. Base64 is accepted by the API, but do not use it here:
  # instances launched from this template get SSH keys, so the platform merges its own
  # cloud-config into the document — and that merge dispatches on the literal
  # #cloud-config prefix. A base64 blob does not carry it, so the blob would be treated
  # as a shell script and the document below would never run, with nothing surfacing in
  # Terraform to say so.
  #
  # cloud-init.yaml, alongside this configuration:
  #
  #   #cloud-config
  #   package_update: true
  #   packages:
  #     - nginx
  #   runcmd:
  #     - [systemctl, enable, --now, nginx]
  #
  # That package step needs the launched instance's VPC to have an outbound path. A
  # VPC with no frostmoln_gateway has no internet and no platform DNS, and cloud-init
  # fails on first boot with nothing in the Terraform plan to explain it.
  #
  # Editing this updates the template; instances already launched from it keep the
  # user data they were created with.
  user_data = file("${path.module}/cloud-init.yaml")

  metadata = {
    role = "web"
  }

  tags = {
    environment = "production"
  }
}
