data "frostmoln_vpc" "production" {
  name = "production-vpc"
}

output "vpc_cidr" {
  value = data.frostmoln_vpc.production.cidr
}
