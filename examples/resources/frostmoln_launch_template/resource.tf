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
  # user data they were created with. Prefer user_data_wo (see the second block
  # below) when the document carries anything secret: user_data is written to
  # Terraform state in plaintext.
  user_data = file("${path.module}/cloud-init.yaml")

  metadata = {
    role = "web"
  }

  tags = {
    environment = "production"
  }
}

# Prefer the write-only form when the document carries anything you would not
# want in the state file. user_data_wo never reaches the plan or state; it needs
# Terraform 1.11 or later. Because Terraform cannot see a write-only value
# change, bumping user_data_wo_version is what sends the current document — and
# unlike frostmoln_instance, that is an in-place update of the template.
resource "frostmoln_launch_template" "bootstrap" {
  name      = "worker-template"
  flavor_id = data.frostmoln_flavor.medium.id
  image_id  = data.frostmoln_image.ubuntu.id
  vpc_id    = frostmoln_vpc.example.id

  # The document is rendered from a template with a secret injected into it.
  # Neither var.bootstrap_token nor the rendered document reaches state — which
  # is the point: where a value is CONSUMED decides whether it is stored, so
  # passing a secret through a variable into user_data would store it in full.
  user_data_wo = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    bootstrap_token = var.bootstrap_token
  })

  # Bumping this sends the current document and updates the template in place.
  # Instances already launched from it keep the user data they were created
  # with. Do not derive it from the document: it is not sensitive, so it is
  # printed verbatim in plan output, CI logs and PR plan comments.
  user_data_wo_version = "1"
}
