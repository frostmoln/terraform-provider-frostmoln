# Associations are imported by the composite id "public_ip_id/instance_id".
# Importing records the attachment only; the address itself stays unmanaged
# unless a frostmoln_public_ip resource is imported separately.
terraform import frostmoln_public_ip_association.web <public-ip-id>/<instance-id>
