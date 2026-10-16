# The data source never exposes the password: manage the user with
# frostmoln_messaging_user to read it.
data "frostmoln_messaging_user" "billing" {
  instance_id = "51455a51-db3d-4231-ac27-4fee2553c15f"
  name        = "billing-app"
}

output "billing_user_status" {
  value = data.frostmoln_messaging_user.billing.status
}
