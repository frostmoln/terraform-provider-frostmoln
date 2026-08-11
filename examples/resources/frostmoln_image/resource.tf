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
# Two create failures are worth recognising, because they share an error code
# and differ only by status. Neither is safe to treat as "wait and re-apply":
#
#   * 403 quota_exceeded — the anti-abuse staging limit on uploads in flight.
#     Its first check counts the images this tenant holds in `queued`, and a
#     FAILED import leaves the image back in `queued` — so abandoned attempts
#     keep holding a slot no matter how long you wait, and only deleting them
#     helps. (Refusals on staged BYTES or objects do drain by themselves once
#     an import settles; nothing in the error says which of the three you hit,
#     so read the message the API returned.)
#   * 409 quota_exceeded — the tenant's custom-image allowance is full. This one
#     never clears on its own. The allowance bounds both the NUMBER of images
#     and their total size, so deleting the smallest one may not be enough;
#     delete enough to free space, or ask for a higher limit. See the destroy
#     note below — that delete can itself be refused.
#
# DESTROY CAN FAIL WITH 409 resource_in_use. Frostmoln stores images on Ceph
# RBD and boots instances from copy-on-write clones of them, so an image the
# storage backend still has clones of cannot be deleted — in practice that means
# instances launched from it. Terraform destroys `frostmoln_instance.app` before
# `frostmoln_image.golden` here, because the `image_id` reference makes that
# dependency explicit — but an instance that takes its image by literal id,
# through a data source, or from another state file carries NO such edge, and
# `terraform destroy` then stops at the image. Add `depends_on` to restore the
# ordering, or destroy the instances first.
#
# The image stays fully intact — the refusal happens before anything is removed,
# so the same destroy succeeds once the last clone is gone.
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
