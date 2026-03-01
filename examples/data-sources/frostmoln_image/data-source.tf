data "frostmoln_image" "ubuntu" {
  name = "ubuntu-24.04"
}

output "ubuntu_image_id" {
  value = data.frostmoln_image.ubuntu.id
}
