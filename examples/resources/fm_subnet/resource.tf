resource "fm_subnet" "example" {
  name        = "web-subnet"
  description = "Web tier subnet"
  cidr        = "10.0.1.0/24"
  vpc_id      = fm_vpc.example.id
  zone        = "sweden-a"
  gateway_ip  = "10.0.1.1"
  dns_servers = ["10.0.0.2"]

  tags = {
    tier = "web"
  }
}
