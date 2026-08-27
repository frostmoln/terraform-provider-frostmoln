# Import a cluster by its ID. public_ip_id is retired: it is refused at plan time
# and was never readable back from the API, so an imported cluster never carries
# it. If a plan then wants to REPLACE the cluster over that attribute, add
# lifecycle { ignore_changes = [public_ip_id] } — removing the line alone is what
# triggers the replacement, not what avoids it.
terraform import frostmoln_kubernetes_cluster.main 51455a51-db3d-4231-ac27-4fee2553c15f
