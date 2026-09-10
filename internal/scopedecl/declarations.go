package scopedecl

// Declarations is the single source of the per-resource authoritative scope —
// rendered into every resource page by Summary and asserted against the live
// provider schema by internal/provider/scope_declarations_test.go. Every
// registered resource has an entry, even an empty one: adding a resource
// without declaring its scope fails the suite.
//
// The reasons matter more than the lists. Immutable paths and Observes labels
// are derivable from the schema (and the test derives them); WHY a field is
// create-immutable, WHAT the platform may do to an observed field, and WHICH
// policy a platform-invented default carries are what the schema cannot say.
//
// Observes is deliberately selective, and its exclusion has a policy: plain
// lifecycle `status` fields are NOT labelled. They are the provider's
// reconciliation read-back doing its job — refresh picks the truth up by
// design — so a label would add noise without honesty. The labels are for
// the fields where out-of-band change or one-time-only issuance would
// otherwise surprise: platform-managed constructs, platform-assigned
// addresses, once-only secrets, platform-metered counters.
var Declarations = map[string]Decl{
	"frostmoln_apache_instance": {
		ImmutableWhy: "the managed offer has no in-place migration for it — a change re-creates the instance",
		Immutable:    fields("php_enabled", "php_version", "subnet_id", "version", "vpc_id"),
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
			{Path: "public_ip", Why: "platform-allocated and attached when `public` is true"},
			{Path: "security_group_id", Why: "the platform creates and owns the managed group; reads succeed but every write is refused (`409 resource_in_use`), permanently"},
		},
		Defaults: []PlatformDefault{
			{
				Name:   "security group provisioned for the managed web server",
				Policy: KeepWithDocs,
				Why:    "Platform-provisioned and platform-owned; do not import it as a `frostmoln_security_group` — applies including destroy are refused",
			},
		},
	},
	"frostmoln_api_key": {
		ImmutableWhy: "the key's expiry is fixed when it is minted",
		Immutable:    fields("expires_at"),
		Observes: []Field{
			{Path: "key", Why: "returned once at create and never again — only this configuration's copy survives; a refresh cannot recover it"},
		},
	},
	"frostmoln_appgw_backend": {
		EnactsNone:   true,
		ImmutableWhy: "the API has no update route for a backend — a change destroys and re-creates it",
		Immutable:    fields("address", "gateway_id", "pool_id", "port", "source_id", "source_kind", "weight"),
	},
	"frostmoln_appgw_backend_authorization": {
		EnactsNone: true,
		Immutable: []Field{
			{Path: "adopt_existing", Why: "it decides whether this resource may take over an ingress rule that already exists — changing it changes what a destroy will do to other backends, so it is create-only"},
			{Path: "backend_id", Why: "the authorization is identified by its backend, pool, gateway and security group; changing one is a different authorization"},
			{Path: "gateway_id", Why: "the authorization is identified by its backend, pool, gateway and security group; changing one is a different authorization"},
			{Path: "pool_id", Why: "the authorization is identified by its backend, pool, gateway and security group; changing one is a different authorization"},
			{Path: "security_group_id", Why: "the authorization is identified by its backend, pool, gateway and security group; changing one is a different authorization"},
		},
	},
	"frostmoln_appgw_backend_pool": {
		Immutable: []Field{
			{Path: "gateway_id", Why: "the pool belongs to its gateway; moving it is a different pool"},
			{Path: "name", Why: "the pool's name is its identity on the gateway"},
		},
	},
	"frostmoln_appgw_certificate": {
		ImmutableWhy: "certificate material and its name are fixed once uploaded — changing one uploads a new certificate",
		Immutable:    fields("chain_pem", "gateway_id", "name", "private_key_pem", "private_key_pem_wo_version"),
		EnactsExcept: []Field{
			{Path: "private_key_pem_wo", Why: "write-only: sent, never stored or read back — the `private_key_pem_wo_version` companion carries change detection"},
		},
	},
	"frostmoln_appgw_config_apply": {
		Immutable:    fields("gateway_id"),
		ImmutableWhy: "the apply belongs to its gateway",
	},
	"frostmoln_appgw_health_check": {
		ImmutableWhy: "the check belongs to its pool on its gateway; moving it is a different check",
		Immutable:    fields("gateway_id", "pool_id"),
	},
	"frostmoln_appgw_listener": {
		EnactsNone:   true,
		ImmutableWhy: "the API has no update route for a listener — the server registers POST, GET and DELETE and nothing else — so any change destroys and re-creates it",
		Immutable: fields("allowed_cidrs", "backend_pool_id", "default_certificate_id", "denied_cidrs",
			"gateway_id", "geo_block_mode", "geo_countries", "max_connections", "name", "port",
			"port_range_end", "protocol", "rate_limit_burst", "rate_limit_rps", "redirect_to_https",
			"sni_certificate_ids", "tls_cipher_profile", "tls_min_version"),
	},
	"frostmoln_appgw_route": {
		EnactsNone:   true,
		ImmutableWhy: "the API has no update route for a route — the server registers POST, GET and DELETE and nothing else — so any change destroys and re-creates it",
		Immutable: fields("action", "backend_pool_id", "gateway_id", "host", "listener_id", "name",
			"path", "path_match_type", "priority", "request_headers_remove", "request_headers_set",
			"response_headers_set", "rewrite_path_prefix"),
	},
	"frostmoln_appgw_waf_exclusion": {
		ImmutableWhy: "the exclusion is identified by its key within its policy; changing one is a different exclusion",
		Immutable:    fields("gateway_id", "policy_id", "rule_key"),
	},
	"frostmoln_appgw_waf_policy": {
		Immutable: []Field{
			{Path: "gateway_id", Why: "the policy belongs to its gateway; moving it is a different policy"},
			{Path: "name", Why: "the policy's name is its identity on the gateway"},
			{Path: "scope", Why: "scope decides what the policy is COMPILED FROM — a gateway policy carries the managed ruleset, an overlay does not; the server's update body has no scope field at all"},
		},
	},
	"frostmoln_appgw_waf_policy_attachment": {
		ImmutableWhy: "the attachment is identified by where it attaches — gateway, listener or route; moving it is a different attachment (`policy_id` itself swaps in place)",
		Immutable:    fields("gateway_id", "listener_id", "route_id"),
	},
	"frostmoln_appgw_waf_policy_publication": {
		ImmutableWhy: "a publication belongs to its policy on its gateway",
		Immutable:    fields("gateway_id", "policy_id"),
	},
	"frostmoln_appgw_waf_rule": {
		ImmutableWhy: "the rule is identified by its key within its policy; changing one is a different rule",
		Immutable:    fields("gateway_id", "policy_id", "rule_key"),
	},
	"frostmoln_application_gateway": {
		ImmutableWhy: "the platform has no in-place migration for it — a change destroys and re-creates the gateway",
		Immutable:    fields("flavor_id", "public_ip_id", "public_ip_mode", "subnet_id", "vpc_id"),
		Observes: []Field{
			{Path: "public_ip", Why: "pool-allocated by the platform when the configuration names no `public_ip_id`"},
			{Path: "version", Why: "platform-managed: chosen by the server at create and upgraded by the platform, never by configuration"},
		},
	},
	"frostmoln_bucket": {
		Immutable: []Field{
			{Path: "name", Why: "the bucket's name IS its identity on the wire — buckets are addressed by name"},
			{Path: "region", Why: "the platform pins it at create; there is no in-place migration"},
			{Path: "storage_class", Why: "the platform pins it at create; there is no in-place migration"},
		},
	},
	"frostmoln_bucket_cors_configuration": {
		ImmutableWhy: "the configuration document belongs to its bucket — the bucket IS the resource's identity",
		Immutable:    fields("bucket"),
	},
	"frostmoln_bucket_lifecycle_configuration": {
		ImmutableWhy: "the configuration document belongs to its bucket — the bucket IS the resource's identity",
		Immutable:    fields("bucket"),
	},
	"frostmoln_container_registry": {
		EnactsNone: true,
		Observes: []Field{
			{Path: "endpoint", Why: "platform-assigned when the registry is provisioned for the tenant"},
			{Path: "namespace", Why: "platform-assigned when the registry is provisioned for the tenant"},
			{Path: "storage_used_bytes", Why: "grows and shrinks as images are pushed and deleted — the platform meters it"},
		},
	},
	"frostmoln_container_registry_cache": {
		ImmutableWhy: "the cache's upstream and its credentials are fixed at create — a change re-creates the cache",
		Immutable:    fields("password", "password_wo_version", "upstream", "username"),
		EnactsExcept: []Field{
			{Path: "password_wo", Why: "write-only: sent, never stored or read back — the `password_wo_version` companion carries change detection"},
		},
	},
	"frostmoln_container_registry_credential": {
		EnactsNone:   true,
		ImmutableWhy: "a registry credential has no update route at all — its capability is fixed when minted, so a change replaces the credential and issues a new secret",
		Immutable:    fields("capability", "name"),
		Observes: []Field{
			{Path: "secret", Why: "returned once at create and never again — a refresh cannot read it back"},
		},
	},
	"frostmoln_dns_record": {
		ImmutableWhy: "a record is identified by its zone, name and type; changing one is a different record",
		Immutable:    fields("name", "type", "zone_id"),
	},
	"frostmoln_dns_zone": {
		ImmutableWhy: "the zone's name is its identity — the zone IS the name",
		Immutable:    fields("name"),
		Observes: []Field{
			{Path: "name_servers", Why: "platform-assigned when the zone is created; delegate to these"},
			{Path: "serial", Why: "the platform bumps it on every record change, including changes made out of band"},
		},
	},
	"frostmoln_gateway": {
		ImmutableWhy: "a gateway belongs to its VPC; moving it is a new gateway",
		Immutable:    fields("vpc_id"),
		EnactsExcept: []Field{
			{Path: "acknowledge_connectivity_loss", Why: "an acknowledgement flag for a destroy that severs the VPC's connectivity — carried in state, not a platform setting the apply pushes"},
		},
		Observes: []Field{
			{Path: "origin", Why: "records whether the gateway was declared or platform-attached (`implicit_public_ip`) — the platform set it, not the configuration"},
			{Path: "source_address", Why: "a platform-attached gateway carries whatever address the platform chose for it"},
		},
	},
	"frostmoln_iam_policy": {},
	"frostmoln_iam_policy_attachment": {
		EnactsNone:   true,
		ImmutableWhy: "the attachment is identified by its policy and attachee; changing one is a different attachment",
		Immutable:    fields("attachee_id", "attachee_type", "policy_id"),
	},
	"frostmoln_image": {
		ImmutableWhy: "the image's content and format are fixed once imported — changing one imports a new image",
		Immutable:    fields("architecture", "container_format", "disk_format", "os_distro", "os_version", "source_file", "source_file_hash"),
	},
	"frostmoln_instance": {
		ImmutableWhy: "the platform has no in-place update for it — a change destroys and re-creates the instance",
		Immutable: []Field{
			{Path: "console_password"},
			{Path: "console_password_wo_version"},
			{Path: "image_id"},
			{Path: "instance_access", Why: "the in-guest agent is installed at first boot; enabling or disabling it later means a new instance"},
			{Path: "ssh_key_names"},
			{Path: "subnet_id"},
			{Path: "user_data", Why: "it runs at first boot only — changing it later cannot affect the running instance, so a change re-creates it"},
			{Path: "user_data_wo_version", Why: "it versions the first-boot user data — bumping it re-creates the instance so the new document runs"},
			{Path: "vpc_id"},
			{Path: "zone", Why: "the platform pins the zone at create; there is no in-place migration between zones"},
		},
		EnactsExcept: []Field{
			{Path: "console_password_wo", Why: "write-only: sent, never stored or read back — the `console_password_wo_version` companion carries change detection"},
			{Path: "user_data_wo", Why: "write-only: sent, never stored or read back — the `user_data_wo_version` companion carries change detection"},
		},
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
			{Path: "public_ip", Why: "platform-attached; an association made out of band is read back, not fought"},
		},
	},
	"frostmoln_instance_port_security_groups": {
		ImmutableWhy: "the resource is identified by its instance and port; changing one is a different attachment",
		Immutable:    fields("instance_id", "port_id"),
	},
	"frostmoln_kubernetes_cluster": {
		ImmutableWhy: "the platform has no in-place migration for it — a change destroys and re-creates the cluster",
		Immutable: []Field{
			{Path: "addons", Why: "adding an addon is a supported in-place day-2 operation, but REMOVING one is not — nothing uninstalls an addon the platform already applied, so a removal forces replacement"},
			{Path: "control_plane_tier"},
			{Path: "initial_node_pool.flavor_id"},
			{Path: "initial_node_pool.name"},
			{Path: "public_ip_id", Why: "retained and deprecated: every new cluster's apiserver is a private VIP and the API refuses any value with a 400 — the attribute exists so a stale configuration is told so"},
			{Path: "region"},
			{Path: "subnet_id"},
			{Path: "version"},
			{Path: "vpc_id"},
		},
		Observes: []Field{
			{Path: "endpoint", Why: "platform-issued once the apiserver is up"},
			{Path: "kubeconfig", Why: "platform-issued once the apiserver is up"},
			{Path: "load_balancer_id", Why: "the platform provisions the load balancer the cluster rides on"},
		},
	},
	"frostmoln_kubernetes_node_pool": {
		Immutable: []Field{
			{Path: "cluster_id", Why: "the pool belongs to its cluster; moving it is a different pool"},
			{Path: "flavor_id", Why: "the platform does not re-flavor a pool in place"},
			{Path: "name", Why: "the pool's name is its identity within the cluster"},
		},
	},
	"frostmoln_launch_template": {
		EnactsExcept: []Field{
			{Path: "user_data_wo", Why: "write-only: sent, never stored or read back — the `user_data_wo_version` companion carries change detection"},
		},
	},
	"frostmoln_lb_health_monitor": {
		ImmutableWhy: "the monitor's identity is its pool and check type; changing one is a different monitor",
		Immutable:    fields("load_balancer_id", "pool_id", "type"),
	},
	"frostmoln_lb_listener": {
		ImmutableWhy: "the listener's identity is its load balancer, protocol and port; changing one is a different listener",
		Immutable:    fields("load_balancer_id", "protocol", "protocol_port"),
	},
	"frostmoln_lb_member": {
		ImmutableWhy: "the member is identified by its pool and address:port; changing one is a different member",
		Immutable: []Field{
			{Path: "address"},
			{Path: "cross_vpc", Why: "a write-only acknowledgement flag preserved in state but never returned by the API — changing it between two known values forces a new member (a first apply that supplies it after import reconciles instead)"},
			{Path: "load_balancer_id"},
			{Path: "pool_id"},
			{Path: "protocol_port"},
			{Path: "subnet_id"},
		},
	},
	"frostmoln_lb_pool": {
		ImmutableWhy: "the pool's identity is its listener, load balancer and protocol; changing one is a different pool",
		Immutable:    fields("listener_id", "load_balancer_id", "protocol"),
	},
	"frostmoln_load_balancer": {
		ImmutableWhy: "the platform has no in-place migration for it — a change destroys and re-creates the load balancer",
		Immutable:    fields("flavor_id", "public_ip_id", "scheme", "subnet_id", "type", "vpc_id"),
		Observes: []Field{
			{Path: "public_ip_address", Why: "platform-allocated when `scheme` is `public` and the configuration names no `public_ip_id`"},
		},
	},
	"frostmoln_messaging_instance": {
		ImmutableWhy: "the platform has no in-place migration for it — a change re-creates the instance",
		Immutable: []Field{
			{Path: "engine", Why: "the engine is chosen at create — one engine's instance does not become another's in place"},
			{Path: "subnet_id"},
			{Path: "version"},
			{Path: "vpc_id"},
		},
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
		},
	},
	"frostmoln_mysql_backup": {
		EnactsNone:   true,
		ImmutableWhy: "a backup is a point-in-time artefact of its instance; changing one is a different backup",
		Immutable:    fields("instance_id", "name", "type"),
	},
	"frostmoln_mysql_instance": {
		ImmutableWhy: "the platform has no in-place migration for it — a change re-creates the instance",
		Immutable:    fields("ha_enabled", "subnet_id", "version", "vpc_id"),
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
			{Path: "public_ip", Why: "platform-assigned where the offer exposes one"},
		},
	},
	"frostmoln_mysql_read_replica": {
		EnactsNone:   true,
		ImmutableWhy: "a replica's source, name and flavor are fixed at create — a change re-creates the replica",
		Immutable:    fields("flavor_id", "instance_id", "name"),
		Observes: []Field{
			{Path: "replication_lag_bytes", Why: "the live replication position as the platform reports it — it moves continuously"},
		},
	},
	"frostmoln_nginx_instance": {
		ImmutableWhy: "the managed offer has no in-place migration for it — a change re-creates the instance",
		Immutable:    fields("php_enabled", "php_version", "subnet_id", "version", "vpc_id"),
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
			{Path: "public_ip", Why: "platform-allocated and attached when `public` is true"},
			{Path: "security_group_id", Why: "the platform creates and owns the managed group; reads succeed but every write is refused (`409 resource_in_use`), permanently"},
		},
		Defaults: []PlatformDefault{
			{
				Name:   "security group provisioned for the managed web server",
				Policy: KeepWithDocs,
				Why:    "Platform-provisioned and platform-owned; do not import it as a `frostmoln_security_group` — applies including destroy are refused",
			},
		},
	},
	"frostmoln_postgres_backup": {
		EnactsNone:   true,
		ImmutableWhy: "a backup is a point-in-time artefact of its instance; changing one is a different backup",
		Immutable:    fields("instance_id", "name", "type"),
	},
	"frostmoln_postgres_instance": {
		ImmutableWhy: "the platform has no in-place migration for it — a change re-creates the instance",
		Immutable:    fields("ha_enabled", "subnet_id", "version", "vpc_id"),
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
			{Path: "public_ip", Why: "platform-assigned where the offer exposes one"},
		},
	},
	"frostmoln_postgres_read_replica": {
		EnactsNone:   true,
		ImmutableWhy: "a replica's source, name and flavor are fixed at create — a change re-creates the replica",
		Immutable:    fields("flavor_id", "instance_id", "name"),
		Observes: []Field{
			{Path: "replication_lag_bytes", Why: "the live replication position as the platform reports it — it moves continuously"},
		},
	},
	"frostmoln_public_ip": {
		EnactsExcept: []Field{
			{Path: "acknowledge_address_loss", Why: "an acknowledgement flag for a destroy that would drop the address — carried in state, not a platform setting the apply pushes"},
		},
		Observes: []Field{
			{Path: "address", Why: "platform-assigned at allocation"},
			{Path: "attachment.kind", Why: "the platform attaches addresses for load balancers, clusters and managed web servers under the customer — an attachment made out of band is read back, not fought"},
			{Path: "attachment.resource_id", Why: "the platform attaches addresses for load balancers, clusters and managed web servers under the customer — an attachment made out of band is read back, not fought"},
			{Path: "attachment.vpc_id", Why: "the platform attaches addresses for load balancers, clusters and managed web servers under the customer — an attachment made out of band is read back, not fought"},
		},
	},
	"frostmoln_public_ip_association": {
		EnactsNone:   true,
		ImmutableWhy: "the attachment is identified by its three endpoints — address, instance and port; changing one is a different attachment",
		Immutable:    fields("instance_id", "port_id", "public_ip_id"),
	},
	"frostmoln_redis_instance": {
		ImmutableWhy: "the platform has no in-place migration for it — a change re-creates the instance",
		Immutable:    fields("subnet_id", "version", "vpc_id"),
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
		},
	},
	"frostmoln_s3_credential": {
		EnactsNone:   true,
		ImmutableWhy: "a credential has no update route — changing what it may do re-mints it and issues a new secret",
		Immutable:    fields("allowed_actions", "allowed_buckets", "description", "ip_whitelist", "name"),
		Observes: []Field{
			{Path: "secret_access_key", Why: "returned once at create and never again — a refresh cannot read it back"},
		},
	},
	"frostmoln_scale_group": {
		Observes: []Field{
			{Path: "current_size", Why: "the autoscaler moves it continuously under the customer — `desired_capacity` is the intent, this is the truth"},
		},
	},
	"frostmoln_secret": {
		ImmutableWhy: "the secret's name is its identity — unique per tenant",
		Immutable:    fields("name"),
		ImmutableWithoutReplace: []Field{
			{Path: "content_type", Why: "a delete is a soft delete and the name stays taken for the recovery window, so replacing would destroy the secret and then fail to re-create it under the same name — a change is warned about at plan and refused at apply instead"},
			{Path: "max_versions", Why: "a delete is a soft delete and the name stays taken for the recovery window, so replacing would destroy the secret and then fail to re-create it under the same name — a change is warned about at plan and refused at apply instead"},
			{Path: "recovery_window_days", Why: "a delete is a soft delete and the name stays taken for the recovery window, so replacing would destroy the secret and then fail to re-create it under the same name — a change is warned about at plan and refused at apply instead"},
		},
		EnactsExcept: []Field{
			{Path: "secret_value", Why: "sensitive: sent, never read back — new versions are what a change creates"},
			{Path: "secret_value_wo", Why: "write-only: sent, never stored or read back — the `secret_value_wo_version` companion carries change detection"},
		},
	},
	"frostmoln_security_group": {
		ImmutableWhy: "the network API has no in-place update for it, so a change destroys and re-creates the group",
		Immutable:    fields("vpc_id"),
		EnactsExcept: []Field{
			{Path: "delete_default_egress", Why: "a create-time directive: it applies when the group is created, and changing it on an existing group is a documented no-op (the plan warns to that effect)"},
		},
		Defaults: []PlatformDefault{
			{
				Name:   "allow-all egress pair (IPv4 + IPv6, empty remote prefix, injected by the network service on every new group)",
				Policy: DeleteOnCreate,
				Why: "The pair is not load-bearing; `delete_default_egress = true` removes it at create " +
					"(opt-in today, the default flips to `true` at provider v2 with a deprecation notice ahead of it)",
			},
			{
				Name:   "the tenant's default security group (`is_default`)",
				Policy: KeepWithDocs,
				Why:    "Readable as the computed `is_default`; no `default_*` resource exists to adopt it today",
			},
		},
	},
	"frostmoln_security_group_rule": {
		EnactsNone:   true,
		ImmutableWhy: "a rule has no in-place update — the platform registers POST and DELETE and nothing else — so a change destroys and re-creates it",
		Immutable: fields("description", "direction", "port_range_max", "port_range_min", "protocol",
			"remote_cidr", "remote_group_id", "security_group_id"),
	},
	"frostmoln_snapshot": {
		EnactsNone:   true,
		ImmutableWhy: "a snapshot is immutable after create — changing any attribute destroys it and takes a new snapshot of the volume",
		Immutable:    fields("description", "name", "tags", "volume_id"),
	},
	"frostmoln_ssh_key": {
		EnactsNone:   true,
		ImmutableWhy: "the platform stores the key material under its name and has no update route for either — a change re-creates the key",
		Immutable:    fields("name", "public_key"),
	},
	"frostmoln_subnet": {
		ImmutableWhy: "the network service has no in-place update for it — a change destroys and re-creates the subnet",
		Immutable: []Field{
			{Path: "cidr"},
			{Path: "dns_servers"},
			{Path: "gateway_ip"},
			{Path: "vpc_id"},
			{Path: "zone", Why: "the platform pins the zone at create; there is no in-place migration between zones"},
		},
	},
	"frostmoln_valkey_instance": {
		ImmutableWhy: "the platform has no in-place migration for it — a change re-creates the instance",
		Immutable:    fields("subnet_id", "version", "vpc_id"),
		Observes: []Field{
			{Path: "private_ip", Why: "platform-assigned from the subnet at create"},
		},
	},
	"frostmoln_volume": {
		ImmutableWhy: "the platform has no in-place migration for it — re-typing, re-zoning or re-encrypting a volume means a new volume",
		Immutable: []Field{
			{Path: "encrypted"},
			{Path: "snapshot_id", Why: "a volume's origin is fixed at create"},
			{Path: "volume_type"},
			{Path: "zone", Why: "the platform pins the zone at create; there is no in-place migration between zones"},
		},
	},
	"frostmoln_volume_attachment": {
		EnactsNone:   true,
		ImmutableWhy: "the attachment is identified by its volume, instance and device path; changing one is a different attachment",
		Immutable:    fields("device_path", "instance_id", "volume_id"),
	},
	"frostmoln_vpc": {
		ImmutableWhy: "the platform does not renumber a VPC in place",
		Immutable:    fields("cidr"),
		Defaults: []PlatformDefault{
			{
				Name:   "the tenant's default VPC (`is_default`)",
				Policy: KeepWithDocs,
				Why:    "Readable as the computed `is_default`; no `default_*` resource exists to adopt it today",
			},
		},
	},
	"frostmoln_vpc_route": {
		EnactsNone:   true,
		ImmutableWhy: "a route has no server-side identity beyond its destination and there is no in-place update for one, so a change destroys and re-creates the route",
		Immutable:    fields("destination", "next_hop", "vpc_id"),
		Defaults: []PlatformDefault{
			{
				Name:   "platform-owned VPC routes (the system routes DNS and managed services ride)",
				Policy: KeepWithDocs,
				Why: "Invisible by design — not listed, cannot be created, cannot be imported; the " +
					"`frostmoln_vpc_routes` data source sees exactly the tenant-visible table",
			},
		},
	},
	"frostmoln_webserver_domain": {
		EnactsNone:   true,
		ImmutableWhy: "the domain binding is identified by its name on its instance; changing one is a different binding",
		Immutable:    fields("domain_name", "instance_id", "is_default", "tls_enabled"),
	},
	"frostmoln_webserver_deployment": {
		ImmutableWhy: "the deployment belongs to its instance",
		Immutable:    fields("instance_id"),
		Observes: []Field{
			{Path: "deploy_id", Why: "platform-issued for each deploy the pipeline runs"},
			{Path: "status", Why: "the platform's deploy pipeline moves it"},
		},
	},
	"frostmoln_workload_identity_binding": {
		ImmutableWhy: "the binding is identified by its cluster, namespace and service account; changing one is a different binding",
		Immutable:    fields("cluster_id", "namespace", "service_account"),
	},
}

// fields builds Immutable entries that share the resource-level ImmutableWhy.
func fields(paths ...string) []Field {
	out := make([]Field, 0, len(paths))
	for _, p := range paths {
		out = append(out, Field{Path: p})
	}
	return out
}
