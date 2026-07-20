resource "frostmoln_subnet" "example" {
  name        = "web-subnet"
  description = "Web tier subnet"
  cidr        = "10.0.1.0/24"
  vpc_id      = frostmoln_vpc.example.id
  zone        = "falkenberg"
  gateway_ip  = "10.0.1.1"

  # dns_servers is optional — omit it to use the platform resolvers. It is
  # ForceNew, so correcting a wrong value later recreates the subnet.

  tags = {
    tier = "web"
  }
}
