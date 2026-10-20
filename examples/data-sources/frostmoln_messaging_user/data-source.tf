# The data source never exposes the password: manage the user with
# frostmoln_messaging_user to read it.
data "frostmoln_messaging_user" "billing" {
  instance_id = "mq_B352H9idUJP6jTWtom31nW"
  name        = "billing-app"
}

output "billing_user_status" {
  value = data.frostmoln_messaging_user.billing.status
}
