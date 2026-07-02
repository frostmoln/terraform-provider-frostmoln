# Import a cluster by its ID. floating_ip_id cannot be recovered (write-only
# on the API) — after import, omit it or add
# lifecycle { ignore_changes = [floating_ip_id] } to avoid a replacement plan.
terraform import frostmoln_kubernetes_cluster.main 51455a51-db3d-4231-ac27-4fee2553c15f
