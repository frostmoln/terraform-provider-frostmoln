# A reusable IAM access policy (ADR-0102). Compose the document with the
# frostmoln_iam_policy_document data source, then attach the policy to a
# principal with frostmoln_iam_policy_attachment.
resource "frostmoln_iam_policy" "ci" {
  name        = "ci-compute-operator"
  description = "CI pipeline: create/read compute in se-sto-1, never delete"
  document    = data.frostmoln_iam_policy_document.ci.json
}
