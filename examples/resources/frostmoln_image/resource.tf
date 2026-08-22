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
# VERIFYING A VENDOR IMAGE. source_file_hash is a change trigger, not a check: it
# is never sent anywhere and nothing compares it to anything. The read-only
# `checksum` attribute is not the check either — it is an MD5 of what the
# platform STORES, and for a qcow2 the import converts the image to raw first,
# so it cannot equal the SHA-256 a vendor publishes. Assert the published value
# locally, against the file itself, before the upload spends the bandwidth — the
# precondition below does that.
#
# That is a check on the SOURCE. In-transit corruption is a separate failure and
# is handled for you: the provider hashes the bytes as it uploads them and
# compares them with the checksum the storage edge reports, failing the apply
# rather than importing a damaged image.
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
# DESTROY CAN ALSO FAIL WITH 409 invalid_state, while the image is still being
# imported. The platform holds the image for the lifetime of its import task, so it
# cannot be deleted mid-import. What clears that is the import FINISHING — after
# which the image is deletable like any other. The platform also expires a
# STALLED import on its own, and reports how long that has left to run, which is
# the number the refusal quotes; because the countdown restarts every time the
# import makes progress, a healthy four-minute import quotes about an hour for
# its whole life and then goes deletable in four minutes.
#
# The provider therefore keeps RETRYING rather than sleeping out the quoted time,
# and does not weigh that time against its own patience before starting — a
# healthy import quotes more than the destroy's whole 60-minute budget for its
# entire life, so weighing it would refuse the one case retrying is for. A
# destroy retries every half minute until the image lets go or the 60 minutes are
# spent; an import that finishes normally is picked up within seconds of
# finishing. If the budget runs out first, the destroy fails with the platform's
# latest quote in the diagnostic — re-run once the import has finished, or after
# that time if it never does. When the platform cannot quote anything at all, the
# destroy fails immediately: re-run shortly (that answer also covers a passing
# failure to read the import) and contact support if it keeps failing.
#
# Note the budget cuts both ways: a destroy against an importing image may now
# BLOCK for up to an hour rather than failing fast, which matters most in CI,
# where a job timeout is what notices.
#
# `terraform import` is deliberately NOT supported for this resource. source_file
# is a local path the API never returns, so an imported image would immediately
# plan a destroy/recreate — re-uploading gigabytes to rebuild something that
# already exists. Declare the image in configuration and apply instead.
locals {
  image_path = "${path.module}/build/golden-ubuntu-24.04.qcow2"
  # The value from the vendor's published SHA256SUMS for exactly this file.
  image_sha256 = "0000000000000000000000000000000000000000000000000000000000000000"
}

resource "frostmoln_image" "golden" {
  name        = "golden-ubuntu-24.04"
  description = "Hardened Ubuntu 24.04 base image"
  source_file = local.image_path
  # One expression serves both purposes: the change trigger that replaces the
  # image when the file is rebuilt, and the value compared below.
  source_file_hash = filesha256(local.image_path)
  disk_format      = "qcow2"

  os_distro    = "ubuntu"
  os_version   = "24.04"
  architecture = "x86_64"

  # Only needed when the platform cannot work the login user out from
  # os_distro. For a distribution it does not recognise, an instance cannot be
  # launched with a console password until this names the user the image
  # creates. Unlike os_distro it is changeable in place, so fixing it later does
  # not replace the image.
  # default_user = "ubuntu"

  min_disk_gb = 20
  min_ram_mb  = 2048

  lifecycle {
    precondition {
      condition     = filesha256(local.image_path) == local.image_sha256
      error_message = "The image at ${local.image_path} does not match the vendor's published SHA-256."
    }
  }
}

resource "frostmoln_instance" "app" {
  name      = "app-01"
  image_id  = frostmoln_image.golden.id
  flavor_id = "gp1.small"
  subnet_id = frostmoln_subnet.example.id
}
