data "frostmoln_subnet" "web" {
  name   = "web-subnet"
  vpc_id = data.frostmoln_vpc.production.id
}

output "subnet_cidr" {
  value = data.frostmoln_subnet.web.cidr
}
