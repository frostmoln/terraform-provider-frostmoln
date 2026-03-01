data "fm_flavors" "compute" {
  category = "compute"
}

output "compute_flavors" {
  value = data.fm_flavors.compute.flavors[*].name
}
