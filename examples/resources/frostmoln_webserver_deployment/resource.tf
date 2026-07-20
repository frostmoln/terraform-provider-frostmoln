# Deploy site content to a managed webserver instance.
#
# source_archive is a LOCAL filesystem path, read at plan time by the machine
# running Terraform at plan and apply time — it is not a bucket key or a URL.
# Terraform tracks the
# archive's content hash, so rebuilding site.tar.gz makes the next apply push a
# new deployment automatically.
#
# Destroying this resource only drops it from Terraform state; content already
# published on the instance stays live (there is no "undeploy" API — destroy the
# instance to remove it).
resource "frostmoln_webserver_deployment" "site" {
  instance_id    = frostmoln_nginx_instance.site.id
  source_archive = "${path.module}/site.tar.gz"
}
