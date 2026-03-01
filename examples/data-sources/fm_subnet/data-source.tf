data "fm_subnet" "web" {
  name   = "web-subnet"
  vpc_id = data.fm_vpc.production.id
}

output "subnet_cidr" {
  value = data.fm_subnet.web.cidr
}
