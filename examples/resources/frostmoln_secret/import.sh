# Secrets are imported by their id.
terraform import frostmoln_secret.database_password <secret-id>

# The provider does not copy the secret value into state on import: secret_value
# is left null so that importing never writes the value to your state file. The
# first apply after an import therefore pushes the value from your configuration
# and writes one new secret version, even when the value is unchanged. Versions
# count against max_versions.

# content_type, max_versions and recovery_window_days are fixed when the secret
# is created, so an imported secret keeps whatever the platform holds. Leave
# them out of the configuration to accept those values, or set them to what
# `terraform state show` reports — a value that disagrees is refused at plan
# time, because the API's update would discard it.
