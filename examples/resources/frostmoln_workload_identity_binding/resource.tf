# A Workload Identity Federation binding: a pod running as the given
# Kubernetes service account can exchange its projected token for a short-lived,
# policy-scoped Frostmoln credential — no long-lived secret in the pod.
#
# Flat scopes are RETIRED (retire-scopes step 2, ADR-0102): a binding's grant is
# its access policies, and a configuration that still sets `scopes` fails at
# apply with 400 BINDING_SCOPES_RETIRED. Remove the attribute; grant with a
# policy below.
resource "frostmoln_workload_identity_binding" "app" {
  cluster_id      = frostmoln_kubernetes_cluster.prod.id
  namespace       = "default"
  service_account = "my-app"
}

# A policy-granted binding: the attachment below is the workload's authority.
# A policy names individual targets, adds constraints and can deny explicitly —
# flat scopes never could grant a verb across every resource of a service.
#
# The binding is INERT until the attachment exists: with no grant at all the
# token exchange refuses it rather than minting a credential that grants nothing.
#
# TEARDOWN — read before you destroy. A granted binding is never left with zero
# grants, so detaching its ONLY policy is refused. `terraform destroy` removes
# the attachment first (it depends on the binding) and therefore fails. Destroy
# the BINDING first; that removes its attachments with it:
#
#   terraform destroy -target=frostmoln_workload_identity_binding.reaper
#   terraform destroy
#
# The same applies to any change that REPLACES the binding (cluster_id, namespace
# and service_account all force replacement).
resource "frostmoln_workload_identity_binding" "reaper" {
  cluster_id      = frostmoln_kubernetes_cluster.prod.id
  namespace       = "ops"
  service_account = "instance-reaper"
}

data "frostmoln_iam_policy_document" "reaper" {
  rule {
    name       = "read-and-delete-instances-only"
    access     = "allow"
    operations = ["compute:instances:read", "compute:instances:list", "compute:instances:delete"]
    targets    = ["frn:compute:*:*:instances/*"]
  }
}

resource "frostmoln_iam_policy" "reaper" {
  name     = "instance-reaper"
  document = data.frostmoln_iam_policy_document.reaper.json
}

resource "frostmoln_iam_policy_attachment" "reaper" {
  policy_id     = frostmoln_iam_policy.reaper.id
  attachee_type = "workload_identity"
  attachee_id   = frostmoln_workload_identity_binding.reaper.id
}
