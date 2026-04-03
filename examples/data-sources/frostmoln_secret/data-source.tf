data "frostmoln_secret" "example" {
  id = "secret-abc123"
}

output "secret_name" {
  value = data.frostmoln_secret.example.name
}
