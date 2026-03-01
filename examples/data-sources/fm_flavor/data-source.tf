data "fm_flavor" "medium" {
  name = "m1.medium"
}

output "medium_flavor_specs" {
  value = "${data.fm_flavor.medium.vcpus} vCPUs, ${data.fm_flavor.medium.ram_mb} MB RAM"
}
