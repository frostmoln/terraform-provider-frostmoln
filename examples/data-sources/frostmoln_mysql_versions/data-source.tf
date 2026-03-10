data "frostmoln_mysql_versions" "available" {}

output "mysql_versions" {
  value = data.frostmoln_mysql_versions.available.versions
}
