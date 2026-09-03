# Secrets are imported by their id.
terraform import frostmoln_secret.database_password <secret-id>

# The provider does not copy the secret value into state on import: secret_value
# is left null so that importing never writes the value to your state file. The
# first apply after an import therefore pushes the value from your configuration
# and writes one new secret version, even when the value is unchanged. Versions
# count against max_versions.
