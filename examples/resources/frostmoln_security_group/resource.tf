resource "frostmoln_security_group" "web" {
  name        = "web-sg"
  description = "Security group for web servers"
  vpc_id      = frostmoln_vpc.example.id

  tags = {
    tier = "web"
  }
}
