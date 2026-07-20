# A Workload Identity Federation binding: a pod running as the given
# Kubernetes service account can exchange its projected token for a short-lived,
# scoped Frostmoln credential — no long-lived secret in the pod.
resource "frostmoln_workload_identity_binding" "app" {
  cluster_id      = frostmoln_kubernetes_cluster.prod.id
  namespace       = "default"
  service_account = "my-app"

  # Least-privilege scopes only. Wildcards ("*" or "<resource>:*") are rejected.
  scopes = [
    "compute:read",
    "storage:read",
  ]
}
