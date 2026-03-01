data "frostmoln_images" "ubuntu_all" {
  os_distro  = "ubuntu"
  name_regex = "^ubuntu-2[24]"
}

output "available_ubuntu_images" {
  value = data.frostmoln_images.ubuntu_all.images[*].name
}
