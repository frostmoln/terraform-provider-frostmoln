data "frostmoln_api_key_scopes" "all" {}

# The scope operation catalog (GET /v1/scopes) — since the API-key scopes
# retirement (retire-scopes step 3, 2026-10-21) this is the AUTHORING
# vocabulary for access policies, not a grant list: any non-empty `scopes` on
# an api-key resource is refused 400 API_KEY_SCOPES_RETIRED at apply.
output "available_scopes" {
  value = { for s in data.frostmoln_api_key_scopes.all.scopes : s.scope => s.description }
}

# A key is granted with an access policy attached to it — zero-scope create is
# the living lane. Compose the document with the frostmoln_iam_policy_document
# data source (the operations it takes are the same operation catalog this
# data source reads), then attach:
#
#   resource "frostmoln_api_key" "ci" {
#     name = "ci-deploy"
#   }
#
#   resource "frostmoln_iam_policy" "ci" {
#     name     = "ci-deploy"
#     document = data.frostmoln_iam_policy_document.ci.json
#   }
#
#   resource "frostmoln_iam_policy_attachment" "ci" {
#     policy_id     = frostmoln_iam_policy.ci.id
#     attachee_type = "api_key"
#     attachee_id   = frostmoln_api_key.ci.id
#   }
