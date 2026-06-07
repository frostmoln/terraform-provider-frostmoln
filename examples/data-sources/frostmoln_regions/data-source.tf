data "frostmoln_regions" "all" {}

output "region_ids" {
  value = data.frostmoln_regions.all.regions[*].id
}
