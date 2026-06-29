resource "fm_vpc" "example" {
  name        = "production-vpc"
  description = "Production VPC"
  cidr        = "10.0.0.0/16"

  tags = {
    environment = "production"
  }
}
