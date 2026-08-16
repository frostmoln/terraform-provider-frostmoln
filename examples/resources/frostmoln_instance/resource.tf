resource "frostmoln_instance" "example" {
  name      = "web-server-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  zone      = "falkenberg"
  vpc_id    = frostmoln_vpc.example.id
  subnet_id = frostmoln_subnet.example.id

  security_groups = [frostmoln_security_group.web.id]
  ssh_key_names   = [frostmoln_ssh_key.example.name]

  # Password for the default OS user, usable only at the VNC console (SSH stays key-only).
  console_password = "change-me-at-the-console" # pragma: allowlist secret

  # Install the Frostmoln in-guest agent at first boot for `fm ssh` terminal access.
  instance_access = true

  # Cloud-init as PLAIN TEXT. Base64 is accepted by the API, but do not use it here:
  # this instance also sets ssh_key_names, console_password and instance_access, so the
  # platform merges its own cloud-config into the document — and that merge dispatches
  # on the literal #cloud-config prefix. A base64 blob does not carry it, so the blob
  # would be treated as a shell script and the document below would never run. The
  # apply still succeeds and the plan stays clean; the only evidence is in the guest's
  # cloud-init log.
  #
  # Changing user_data REPLACES the instance, and the change-detection hash is taken
  # over the value as written — so moving a live instance off base64encode(file(...))
  # plans a replacement even though the document itself is unchanged.
  #
  # cloud-init.yaml, alongside this configuration:
  #
  #   #cloud-config
  #   package_update: true
  #   packages:
  #     - nginx
  #   write_files:
  #     - path: /var/www/html/index.html
  #       content: |
  #         <h1>web-server-01</h1>
  #   runcmd:
  #     - [systemctl, enable, --now, nginx]
  #
  # That package step needs the VPC to have an outbound path. A VPC with no
  # frostmoln_gateway has no internet and no platform DNS, and cloud-init fails on
  # first boot with nothing in the Terraform plan to explain it.
  user_data = file("${path.module}/cloud-init.yaml")

  tags = {
    role        = "web"
    environment = "production"
  }
}
