# Upload a custom disk image (bring-your-own-image) and boot an instance from it.
#
# source_file is a LOCAL filesystem path, read by the machine running Terraform —
# it is not a bucket key or a URL. The provider creates the image record, uploads
# the file straight to Frostmoln object storage with a presigned form, asks the
# platform to import it, and waits for it to reach "active".
#
# Custom images require the `custom-images` entitlement on the tenant. Without
# it the API refuses the create.
#
# Terraform cannot see a change to the CONTENTS of source_file under an unchanged
# path, so pair it with source_file_hash when you rebuild the image in place — a
# changed hash replaces the image, which re-uploads and re-imports it.
#
# Two failures are worth recognising, because they share an error code and have
# opposite remedies:
#
#   * 403 quota_exceeded — the transient anti-abuse limit on uploads in flight.
#     It clears on its own; apply again in a few minutes.
#   * 409 quota_exceeded — the tenant's custom-image allowance is full. It does
#     NOT clear on its own; delete an existing custom image first.
#
# `terraform import` is deliberately NOT supported for this resource. source_file
# is a local path the API never returns, so an imported image would immediately
# plan a destroy/recreate — re-uploading gigabytes to rebuild something that
# already exists. Declare the image in configuration and apply instead.
resource "frostmoln_image" "golden" {
  name             = "golden-ubuntu-24.04"
  description      = "Hardened Ubuntu 24.04 base image"
  source_file      = "${path.module}/build/golden-ubuntu-24.04.qcow2"
  source_file_hash = filemd5("${path.module}/build/golden-ubuntu-24.04.qcow2")
  disk_format      = "qcow2"

  os_distro    = "ubuntu"
  os_version   = "24.04"
  architecture = "x86_64"

  min_disk_gb = 20
  min_ram_mb  = 2048
}

resource "frostmoln_instance" "app" {
  name      = "app-01"
  image_id  = frostmoln_image.golden.id
  flavor_id = "gp1.small"
  subnet_id = frostmoln_subnet.example.id
}
