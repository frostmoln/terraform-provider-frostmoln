# Import an existing egress gateway. The resource is one-to-one with a VPC, so
# either identifier works: the gateway's own id, or the id of the VPC it serves.
# (A gateway with no stored record — origin "legacy" — is only addressable by
# its VPC id.)
terraform import frostmoln_egress_gateway.example vpc-abc123

# After importing, set acknowledge_connectivity_loss = true in the configuration
# before you can change the gateway's mode or destroy it: an imported gateway is
# never pre-acknowledged.
