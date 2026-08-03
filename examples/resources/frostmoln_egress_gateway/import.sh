# Import an existing egress gateway by its VPC id. The resource is one-to-one
# with a VPC, so the gateway's id is the VPC's id.
terraform import frostmoln_egress_gateway.example vpc-abc123
