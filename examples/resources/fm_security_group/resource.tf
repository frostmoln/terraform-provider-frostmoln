resource "fm_security_group" "web" {
  name        = "web-sg"
  description = "Security group for web servers"
  vpc_id      = fm_vpc.example.id

  tags = {
    tier = "web"
  }
}
