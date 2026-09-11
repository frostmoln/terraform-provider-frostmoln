data "frostmoln_database_types" "available" {}

output "supported_types" {
  value = data.frostmoln_database_types.available.types
}
