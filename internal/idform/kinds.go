package idform

// Kinds maps each resource type to the ids its state upgrader rewrites: a
// dotted attribute path (element steps omitted) to a template of servicekit
// prefixes (see rewrite). Every entry was checked against the backend edge
// specs on 2026-10-20. Only ids whose kind is certain are listed. An id left
// out stays a UUID in state, which is safe: the API accepts it, the replace
// guard compares a UUID by bytes alone, and Read refreshes a computed-only id
// to the API's spelling. Left out on purpose:
//   - ids whose kind depends on a sibling field: iam_policy_attachment
//     attachee_id (attachee_type), public_ip attachment.resource_id (kind),
//     appgw_backend source_id (source_kind);
//   - ids the API never encodes: lb_listener tls_certificate_id (opaque), and
//     launch_template ssh_key_ids (names);
//   - ids public-format responses omit: kubernetes_cluster load_balancer_id
//     (an enclave interior id; null under public ids), and the appgw health
//     check's row id (the resource is keyed gateway/pool instead, which Read
//     rebuilds).
var Kinds = map[string]map[string]string{
	"frostmoln_apache_instance":               {"id": "ws", "vpc_id": "vpc", "subnet_id": "sub", "tenant_id": "ten", "security_group_id": "sg"},
	"frostmoln_nginx_instance":                {"id": "ws", "vpc_id": "vpc", "subnet_id": "sub", "tenant_id": "ten", "security_group_id": "sg"},
	"frostmoln_api_key":                       {"id": "key"},
	"frostmoln_appgw_backend_authorization":   {"id": "aga", "gateway_id": "agw", "pool_id": "agp", "backend_id": "agb", "security_group_id": "sg"},
	"frostmoln_appgw_backend_pool":            {"id": "agp", "gateway_id": "agw"},
	"frostmoln_appgw_backend":                 {"id": "agb", "gateway_id": "agw", "pool_id": "agp"},
	"frostmoln_appgw_certificate":             {"id": "crt", "gateway_id": "agw", "dns_zone_id": "dnz"},
	"frostmoln_appgw_config_apply":            {"id": "agw:-", "gateway_id": "agw"},
	"frostmoln_appgw_health_check":            {"gateway_id": "agw", "pool_id": "agp"},
	"frostmoln_appgw_listener":                {"id": "agl", "gateway_id": "agw", "backend_pool_id": "agp", "default_certificate_id": "crt", "sni_certificate_ids": "crt", "waf_policy_id": "waf"},
	"frostmoln_appgw_route":                   {"id": "agr", "gateway_id": "agw", "listener_id": "agl", "backend_pool_id": "agp", "waf_policy_id": "waf"},
	"frostmoln_appgw_waf_exclusion":           {"gateway_id": "agw", "policy_id": "waf"},
	"frostmoln_appgw_waf_rule":                {"gateway_id": "agw", "policy_id": "waf"},
	"frostmoln_appgw_waf_policy":              {"id": "waf", "gateway_id": "agw"},
	"frostmoln_appgw_waf_policy_attachment":   {"id": "agw|agw/agl|agw/agl/agr", "gateway_id": "agw", "listener_id": "agl", "route_id": "agr", "policy_id": "waf"},
	"frostmoln_appgw_waf_policy_publication":  {"id": "waf/-", "policy_id": "waf", "gateway_id": "agw", "dry_run_id": "wdr"},
	"frostmoln_application_gateway":           {"id": "agw", "vpc_id": "vpc", "subnet_id": "sub", "public_ip_id": "pip", "waf_policy_id": "waf"},
	"frostmoln_container_registry":            {"id": "ten"},
	"frostmoln_container_registry_cache":      {"tenant_id": "ten"},
	"frostmoln_container_registry_retention":  {"id": "ten"},
	"frostmoln_dns_record":                    {"id": "dnr", "zone_id": "dnz"},
	"frostmoln_dns_zone":                      {"id": "dnz"},
	"frostmoln_gateway":                       {"id": "vpc", "vpc_id": "vpc", "public_ip_id": "pip"},
	"frostmoln_iam_policy":                    {"id": "pol", "tenant_id": "ten"},
	"frostmoln_iam_policy_attachment":         {"id": "pol/*", "policy_id": "pol"},
	"frostmoln_image":                         {"id": "img"},
	"frostmoln_instance":                      {"id": "ins", "image_id": "img", "vpc_id": "vpc", "subnet_id": "sub", "security_groups": "sg"},
	"frostmoln_instance_port_security_groups": {"id": "ins/nic", "instance_id": "ins", "port_id": "nic", "security_groups": "sg"},
	"frostmoln_kubernetes_cluster":            {"id": "k8s", "vpc_id": "vpc", "subnet_id": "sub", "public_ip_id": "pip", "initial_node_pool.id": "np", "tenant_id": "ten"},
	"frostmoln_kubernetes_node_pool":          {"id": "np", "cluster_id": "k8s"},
	"frostmoln_launch_template":               {"id": "lt", "image_id": "img", "vpc_id": "vpc", "security_group_ids": "sg"},
	"frostmoln_lb_health_monitor":             {"id": "lbh", "load_balancer_id": "lb", "pool_id": "lbp"},
	"frostmoln_lb_listener":                   {"id": "lbl", "load_balancer_id": "lb", "default_pool_id": "lbp"},
	"frostmoln_lb_member":                     {"id": "lbm", "load_balancer_id": "lb", "pool_id": "lbp", "subnet_id": "sub"},
	"frostmoln_lb_pool":                       {"id": "lbp", "load_balancer_id": "lb", "listener_id": "lbl"},
	"frostmoln_load_balancer":                 {"id": "lb", "vpc_id": "vpc", "subnet_id": "sub", "public_ip_id": "pip", "vip_port_id": "nic"},
	"frostmoln_messaging_instance":            {"id": "mq", "vpc_id": "vpc", "subnet_id": "sub"},
	"frostmoln_messaging_permission":          {"id": "mq/*", "instance_id": "mq"},
	"frostmoln_messaging_user":                {"id": "mq/*", "instance_id": "mq"},
	"frostmoln_messaging_vhost":               {"id": "mq/*", "instance_id": "mq"},
	"frostmoln_mysql_backup":                  {"id": "dbb", "instance_id": "db"},
	"frostmoln_mysql_instance":                {"id": "db", "vpc_id": "vpc", "subnet_id": "sub", "parameter_group_id": "dbp", "tenant_id": "ten"},
	"frostmoln_mysql_read_replica":            {"id": "dbr", "instance_id": "db"},
	"frostmoln_postgres_backup":               {"id": "dbb", "instance_id": "db"},
	"frostmoln_postgres_instance":             {"id": "db", "vpc_id": "vpc", "subnet_id": "sub", "parameter_group_id": "dbp", "tenant_id": "ten", "restore_from.source_instance_id": "db", "restore_from.backup_id": "dbb"},
	"frostmoln_postgres_read_replica":         {"id": "dbr", "instance_id": "db"},
	"frostmoln_public_ip":                     {"id": "pip", "instance_id": "ins", "attachment.vpc_id": "vpc"},
	"frostmoln_public_ip_association":         {"id": "pip/ins", "public_ip_id": "pip", "instance_id": "ins", "port_id": "nic"},
	"frostmoln_redis_instance":                {"id": "cch", "vpc_id": "vpc", "subnet_id": "sub"},
	"frostmoln_valkey_instance":               {"id": "cch", "vpc_id": "vpc", "subnet_id": "sub"},
	"frostmoln_scale_group":                   {"id": "asg", "launch_template_id": "lt", "subnet_ids": "sub", "load_balancer_pool_ids": "lbp"},
	"frostmoln_secret":                        {"id": "sec"},
	"frostmoln_security_group":                {"id": "sg", "vpc_id": "vpc"},
	"frostmoln_security_group_rule":           {"id": "sgr", "security_group_id": "sg", "remote_group_id": "sg"},
	"frostmoln_snapshot":                      {"id": "vsn", "volume_id": "vol"},
	"frostmoln_subnet":                        {"id": "sub", "vpc_id": "vpc"},
	"frostmoln_tag_color":                     {"id": "tcr", "organization_id": "org"},
	"frostmoln_tenant_default_tags":           {"id": "ten", "tenant_id": "ten"},
	"frostmoln_volume":                        {"id": "vol", "snapshot_id": "vsn", "attached_to": "ins"},
	"frostmoln_volume_attachment":             {"id": "vol/ins", "volume_id": "vol", "instance_id": "ins"},
	"frostmoln_vpc":                           {"id": "vpc"},
	"frostmoln_vpc_route":                     {"id": "vpc/*", "vpc_id": "vpc"},
	"frostmoln_webserver_deployment":          {"id": "ws", "instance_id": "ws", "deploy_id": "wsd"},
	"frostmoln_webserver_domain":              {"id": "wsdm", "instance_id": "ws"},
	"frostmoln_workload_identity_binding":     {"id": "wib", "cluster_id": "k8s", "tenant_id": "ten"},
}
