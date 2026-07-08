# Compose a Frostmoln IAM access-policy document (ADR-0102) from rule blocks.
# This is a pure local computation — no API call — and its `json` output is
# assigned to a frostmoln_iam_policy resource's `document`.
data "frostmoln_iam_policy_document" "ci" {
  # Allow a CI key to create and read compute instances in one region, and only
  # from the office network. The source-IP constraint is on the ALLOW rule, so
  # the grant itself is network-restricted.
  rule {
    name       = "compute-create-read-from-office"
    access     = "allow"
    operations = ["compute:instances:create", "compute:instances:read", "compute:instances:list"]
    targets    = ["frn:compute:se-sto-1:*:instances/*"]

    constraint {
      operator = "equals"
      key      = "frn:region"
      values   = ["se-sto-1"]
    }
    constraint {
      operator = "ipInRange"
      key      = "frn:sourceIp"
      values   = ["203.0.113.0/24"]
    }
  }

  # Never delete — an UNCONSTRAINED deny. A constraint here would only forbid
  # delete when the constraint held, leaving it permitted otherwise.
  rule {
    name       = "no-delete"
    access     = "deny"
    operations = ["compute:instances:delete"]
    targets    = ["*"]
  }
}

output "policy_json" {
  value = data.frostmoln_iam_policy_document.ci.json
}
