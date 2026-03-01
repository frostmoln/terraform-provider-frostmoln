data "frostmoln_flavors" "compute" {
  category = "compute"
}

output "compute_flavors" {
  value = data.frostmoln_flavors.compute.flavors[*].name
}
