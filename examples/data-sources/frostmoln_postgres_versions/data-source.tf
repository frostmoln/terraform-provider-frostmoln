data "frostmoln_postgres_versions" "available" {}

output "postgres_versions" {
  value = data.frostmoln_postgres_versions.available.versions
}
