data "fm_vpc" "production" {
  name = "production-vpc"
}

output "vpc_cidr" {
  value = data.fm_vpc.production.cidr
}
