data "frostmoln_database_engines" "available" {}

output "supported_engines" {
  value = data.frostmoln_database_engines.available.engines
}
