data "frostmoln_kubernetes_cluster_addons" "main" {
  cluster_id = frostmoln_kubernetes_cluster.main.id
}

# Pins that went stale: the cluster keeps running them, but the version is
# deprecated or end of life. addons is null when no version data is available
# (for example while the cluster is still being created).
output "stale_addon_pins" {
  value = [
    for a in coalesce(data.frostmoln_kubernetes_cluster_addons.main.addons, []) :
    a.key if contains(["deprecated", "eol"], a.pinned_status)
  ]
}
