data "frostmoln_flavor" "medium" {
  name = "m1.medium"
}

output "medium_flavor_specs" {
  value = "${data.frostmoln_flavor.medium.vcpus} vCPUs, ${data.frostmoln_flavor.medium.ram_mb} MB RAM"
}
