# S3 credentials are imported by their access key ID.
# secret_access_key is NOT recoverable on import - the API returns it only when
# the credential is created. Rotate or replace the credential if you need it.
terraform import frostmoln_s3_credential.example <access-key-id>
