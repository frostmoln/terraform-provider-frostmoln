# Resolve a block volume that Terraform did not create
# (provisioned in the portal or with `fm`) by NAME — no hardcoded UUID.
# (A bucket's name IS its id; a volume's id is distinct from its name, so a
# resolved id is what every other reference takes.)
data "frostmoln_volume" "data_disk" {
  name = "billing-data"
}

# The resolved volume feeds references: attach the out-of-band volume to an
# instance this configuration manages, pinning the device path.
resource "frostmoln_volume_attachment" "data_disk" {
  volume_id   = data.frostmoln_volume.data_disk.id
  instance_id = frostmoln_instance.billing.id
  device_path = "/dev/vdb"
}

# attachments shows what consumes the volume RIGHT NOW — one row per consuming
# instance, with the device path it occupies. Fail a plan when the data disk
# is either consumed unexpectedly or left unconsumed.
check "billing_data_disk_is_attached" {
  assert {
    condition     = length(data.frostmoln_volume.data_disk.attachments) == 1
    error_message = "the billing-data volume must be attached to exactly one instance"
  }
}

# A name that matches nothing — or more than one volume — fails the read
# loudly rather than picking a volume for you.
