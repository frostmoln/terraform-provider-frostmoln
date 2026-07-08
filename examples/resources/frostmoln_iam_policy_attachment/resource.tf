# Attach an IAM policy to a machine principal (an API key or workload identity)
# or a group. The attachment is immutable — changing any field re-attaches.
resource "frostmoln_iam_policy_attachment" "ci_key" {
  policy_id     = frostmoln_iam_policy.ci.id
  attachee_type = "api_key" # or "workload_identity" / "group"
  attachee_id   = frostmoln_api_key.ci.id
}
