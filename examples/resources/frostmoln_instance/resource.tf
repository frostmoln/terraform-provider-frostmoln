resource "frostmoln_instance" "example" {
  name      = "web-server-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  zone      = "falkenberg"
  vpc_id    = frostmoln_vpc.example.id
  subnet_id = frostmoln_subnet.example.id

  security_groups = [frostmoln_security_group.web.id]
  ssh_key_names   = [frostmoln_ssh_key.example.name]

  # Password for the default OS user, usable only at the VNC console (SSH stays
  # key-only). Written to Terraform state in plaintext — prefer
  # console_password_wo (see the second block below).
  console_password = "change-me-at-the-console" # pragma: allowlist secret

  # Install the Frostmoln in-guest agent at first boot for `fm ssh` terminal access.
  instance_access = true

  # Prefer user_data_wo (see the second block below) when this document carries
  # anything secret: user_data is written to Terraform state in plaintext.
  #
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

# Prefer the write-only form when the document carries anything you would not
# want in the state file. user_data_wo never reaches the plan or state; it needs
# Terraform 1.11 or later. Because Terraform cannot see a write-only value
# change, bumping user_data_wo_version is what launches a replacement with the
# current document — user data is only ever read at first boot, so this is the
# same lifecycle a change to user_data has.
resource "frostmoln_instance" "bootstrap" {
  name      = "worker-01"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.example.id
  subnet_id = frostmoln_subnet.example.id

  # The document is rendered from a template with a secret injected into it.
  # Neither var.bootstrap_token nor the rendered document reaches state — which
  # is the point: where a value is CONSUMED decides whether it is stored, so
  # passing a secret through a variable into user_data would store it in full.
  user_data_wo = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    bootstrap_token = var.bootstrap_token
  })

  # Bumping this REPLACES the instance with one launched from the current
  # document. Do not derive it from the document: it is not sensitive, so it is
  # printed verbatim in plan output, CI logs and PR plan comments.
  user_data_wo_version = "1"

  # console_password has a write-only form too, with the same shape and the same
  # replace-on-version-bump lifecycle — the password is seeded at first boot and
  # never again, so there is nothing an in-place update could change.
  console_password_wo         = var.console_password
  console_password_wo_version = "1"
}
