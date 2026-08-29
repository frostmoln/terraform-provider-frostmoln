# Changelog

All notable changes to this project will be documented in this file.

## [0.43.0] - 2026-08-29

### 🚀 Features

- *(registry)* Show the storage cap a push is refused against (#411)

## [0.42.0] - 2026-08-29

### 🚀 Features

- *(appgw)* Make terraform apply actually apply the gateway configuration (#409)

## [0.41.0] - 2026-08-29

### 🚀 Features

- *(appgw)* [**breaking**] The flavor catalog describes the appliance, not its VM (#407)

### 🐛 Bug Fixes

- *(appgw)* [**breaking**] Read the flavor catalog per tenant; version becomes read-only (#405)

## [0.40.0] - 2026-08-28

### 🚀 Features

- *(registry)* Container registry resources and data source (#403)

## [0.39.0] - 2026-08-28

### 🚀 Features

- *(appgw)* Add the Application Gateway resources and data sources (#401)

## [0.38.1] - 2026-08-27

### 🐛 Bug Fixes

- *(ci)* Never let a release rerun rewind the public GitHub mirror (#397)

### 📚 Documentation

- *(k8s)* Stop naming external-secrets as the platform default addon (#399)

## [0.38.0] - 2026-08-27

### 🚀 Features

- *(kubernetes_cluster)* [**breaking**] Refuse public_ip_id at plan time; stop sending it (#395)

## [0.37.10] - 2026-08-27

### 🐛 Bug Fixes

- *(network)* In-place updates are PUT, not PATCH — nothing routes PATCH (#393)

## [0.37.9] - 2026-08-26

### 🐛 Bug Fixes

- *(iam)* Honour provider tenant_id for policies, attachments and bindings (#391)

## [0.37.8] - 2026-08-26

### 🐛 Bug Fixes

- *(gateway)* Name every cause of the default-route refusal diagnostic (#389)

## [0.37.7] - 2026-08-26

### 🐛 Bug Fixes

- *(instance)* Resize awaited a status vocabulary the API never speaks, and raced its own workflows (#387)

## [0.37.6] - 2026-08-26

### 🐛 Bug Fixes

- *(vpc_route)* ValidateConfig was inert for the config it exists to warn about (#385)

### 📚 Documentation

- Drop references to a withdrawn ADR (#383)

## [0.37.5] - 2026-08-25

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.18 (#379)
- *(client)* Require a flat envelope for IsNotFound so a routing 404 stops destroying state (#381)

## [0.37.4] - 2026-08-24

### 🐛 Bug Fixes

- *(vpc_route)* Never drop state on a gateway routing failure (#377)

## [0.37.3] - 2026-08-24

### 🐛 Bug Fixes

- *(examples)* Stop publishing the storage backend to the Terraform Registry (#373)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.10.0 (#375)

## [0.37.2] - 2026-08-23

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.17 (#368)
- *(lb)* Stop nulling a configured flavor_id when the API omits it (#371)

### 📚 Documentation

- *(image)* BYOI no longer requires the custom-images entitlement (#367)

## [0.37.1] - 2026-08-23

### ⚙️ Miscellaneous Tasks

- *(lb)* Drop the legacy provider compatibility from the live paths (#365)

## [0.37.0] - 2026-08-23

### 🚀 Features

- *(lb)* Rename provider_type to type; values l7/l4; add state upgrader (#363)

### 🐛 Bug Fixes

- *(docs)* Remove internal technology names from published schema descriptions (#361)

## [0.36.2] - 2026-08-22

### ⚙️ Miscellaneous Tasks

- Replace the NordicLight WIP name with Frostmoln (#357)

## [0.36.1] - 2026-08-22

### 🐛 Bug Fixes

- *(image)* Validate architecture at plan time, x86_64 only (#358)

## [0.36.0] - 2026-08-22

### 🚀 Features

- *(image)* Add the default_user attribute (#355)

## [0.35.0] - 2026-08-21

### 🚀 Features

- *(vpc_route)* Warn at plan time that a default route takes Public IPs down (#353)

## [0.34.1] - 2026-08-20

### 🐛 Bug Fixes

- *(vpc_route)* Operation-aware diagnostics on the Delete path (#351)

## [0.34.0] - 2026-08-20

### 🚀 Features

- *(vpc_route)* Add the frostmoln_vpc_route resource (#349)

## [0.33.0] - 2026-08-20

### 🚀 Features

- *(image)* Name the reason an import failed (#343)

### 📚 Documentation

- *(gateway)* Stop naming the retired cluster ingress in the example (#347)

## [0.32.0] - 2026-08-20

### 🚀 Features

- *(kubernetes_cluster)* [**breaking**] Remove the worker ingress load balancer attributes (#344)

## [0.31.0] - 2026-08-18

### 🚀 Features

- *(image)* Retry a destroy while an import still holds the image (#341)

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.16 (#339)

## [0.30.3] - 2026-08-17

### 🐛 Bug Fixes

- *(images)* Wait out the platform's concurrent-import cap instead of failing the apply (#337)

## [0.30.2] - 2026-08-17

### 📚 Documentation

- *(gateway-ordering)* Complete the set of attaching surfaces, and guard it (#335)

## [0.30.1] - 2026-08-17

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.15 (#332)

### 📚 Documentation

- *(public-ip)* Say where the gateway depends_on goes, and correct what it prevents (#329)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.9.4 (#331)

## [0.30.0] - 2026-08-16

### 🚀 Features

- *(public-ip)* Attach an already-reserved Public IP to an instance (#326)

### 📚 Documentation

- *(provider)* A Terraform VPC is isolated, user_data must be plain text, and SG rule drift is invisible (#325)

## [0.29.0] - 2026-08-15

### 🚀 Features

- *(provider)* Say which credential the provider authenticated with (#323)

## [0.28.3] - 2026-08-15

### 🐛 Bug Fixes

- *(clicreds)* Stop deleting the fm CLI's per-context tenant on token write-back (#321)

## [0.28.2] - 2026-08-15

### 🐛 Bug Fixes

- *(provider)* Default the API endpoint to the gateway's /api prefix (#319)

## [0.28.1] - 2026-08-15

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.14 (#316)
- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.14 (#316)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.9.3 (#315)

## [0.28.0] - 2026-08-14

### 🚀 Features

- *(datasource)* Add frostmoln_api_key_scopes for scope discovery (#313)

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.13 (#309)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.9.2 (#311)

## [0.27.1] - 2026-08-13

### 🐛 Bug Fixes

- *(client)* Poll the tenant-scoped operations route (#303)
- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.12 (#306)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.9.0 (#301)
- *(deps)* Update dependency frostmoln/workflows to v0.9.1 (#305)

## [0.27.0] - 2026-08-12

### 🚀 Features

- *(image)* Refuse the import when the uploaded bytes did not arrive intact (#299)

## [0.26.0] - 2026-08-11

### 🚀 Features

- *(kubernetes_cluster)* [**breaking**] Ingress_scheme / ingress_public_ip_id; remove scheme (#296)

### 📚 Documentation

- *(image)* Correct the 403 caveat and document the destroy 409 (#295)

## [0.25.0] - 2026-08-11

### 🚀 Features

- *(workload-identity-binding)* Make scopes optional for policy-granted bindings (#293)

## [0.24.0] - 2026-08-09

### 🚀 Features

- *(lb)* Give lb_pool and lb_health_monitor a tags attribute (#287)

### ⚙️ Miscellaneous Tasks

- *(gateway)* Stop documenting a mode and a name nobody ever used (#289)
- *(gateway)* Stop shipping a mode and a name that no longer exist (#291)

## [0.23.2] - 2026-08-09

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.11 (#283)

### 🚜 Refactor

- *(gateway)* Rename frostmoln_egress_gateway to frostmoln_gateway (#285)

## [0.23.1] - 2026-08-07

### 🐛 Bug Fixes

- *(instance)* Let an emptied tags block actually clear the instance's tags (#281)

## [0.23.0] - 2026-08-07

### 🚀 Features

- *(image)* Add the frostmoln_image resource (#277)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.8.6 (#275)
- *(egress)* Stop offering the withdrawn NAT egress mode (ADR-0114) (#279)

## [0.22.1] - 2026-08-07

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.10 (#273)

## [0.22.0] - 2026-08-06

### 🚀 Features

- *(client)* Carry the operation's errorCode (#271)

## [0.21.0] - 2026-08-06

### 🚀 Features

- *(egress-gateway)* Public_ip_id, and refuse to silently lose a pinned address (#269)

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.9 (#266)
- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.9 (#266)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.8.5 (#265)

## [0.20.0] - 2026-08-05

### 🚀 Features

- Frostmoln_egress_gateway resource (#247)
- *(bucket)* Expose bucket CORS and lifecycle configuration (#259)
- *(egress-gateway)* Nat mode, acknowledged removal and a VPC lookup (#263)
- *(egress-gateway)* Nat mode, acknowledged removal and a VPC lookup (#263)

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.8 (#249)
- *(deps)* Update module github.com/hashicorp/terraform-plugin-log to v0.11.0 (#250)
- *(provider)* Retire the dead nl.* flavor names from tests and acceptance config (#253)
- *(ci)* Restrict workflow_dispatch to refs/heads/main (#257)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.8.3 (#255)
- *(deps)* Update dependency frostmoln/workflows to v0.8.4 (#261)

## [0.19.5] - 2026-08-03

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.7 (#239)
- *(client)* Decode the error envelope's details as an object, not a string (#243)

### 📚 Documentation

- *(backup)* Point the mirrored backup defaults at servicekit (#241)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.8.0 (#237)
- *(deps)* Update dependency frostmoln/workflows to v0.8.2 (#245)

## [0.19.4] - 2026-08-01

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.6 (#235)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.7.4 (#233)

## [0.19.3] - 2026-07-31

### 🐛 Bug Fixes

- *(db)* Pin server backup policy on postgres+mysql instances (#231)

## [0.19.2] - 2026-07-29

### 🐛 Bug Fixes

- *(instance)* Report the instance's real VPC id in the data source (#229)

## [0.19.1] - 2026-07-26

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.5 (#227)

## [0.19.0] - 2026-07-26

### 🚀 Features

- *(redis)* Add backup_enabled/schedule/retention_days attributes (#219)

### 🐛 Bug Fixes

- *(provider)* Route webserver config changes to PUT /webservers/{id}/config (#217)
- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.4 (#223)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.6.3 (#221)
- *(deps)* Update dependency frostmoln/workflows to v0.7.0 (#224)

## [0.18.0] - 2026-07-21

### 🚀 Features

- *(iam)* Reject region-pinned policy targets at plan time (#215)

### 🐛 Bug Fixes

- *(webserver)* Php_enabled and php_version force a replacement (#211)
- *(provider)* Order RequiresReplace after UseStateForUnknown on 26 attributes (#213)

### 📚 Documentation

- *(examples)* Add Example Usage for the 11 resources and data sources missing one (#209)

## [0.17.2] - 2026-07-21

### 🐛 Bug Fixes

- *(s3_credential)* Decode the credential id from accessKeyId (#207)

## [0.17.1] - 2026-07-20

### 📚 Documentation

- Remove internal ADR refs from customer-facing schema descriptions (#205)

## [0.17.0] - 2026-07-15

### 🚀 Features

- *(iam)* Single-attachment lookup for policy_attachment reads (#203)

## [0.16.0] - 2026-07-10

### 🚀 Features

- *(client)* Retry transient 409 on managed-DB primary resize (#192)
- *(iam)* Terraform IAM policy resources + policy-document data source (#196)

### 🐛 Bug Fixes

- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.1 (#190)
- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.2 (#199)
- *(deps)* Update module go.frostmoln.internal/oidc to v0.3.3 (#201)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.6.1 (#194)
- *(deps)* Update dependency frostmoln/workflows to v0.6.2 (#198)

## [0.15.1] - 2026-07-06

### 🚜 Refactor

- Remove dead floating_ip state-upgraders (no TF users, breaking) (#188)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.6.0 (#185)
- Reuse shared go-build.yml workflow (test + coverage only) (#184)

## [0.15.0] - 2026-07-05

### 🚀 Features

- *(nginx)* Add php_enabled + php_version to nginx_instance (#179)
- *(cache)* In-place flavor_id resize for redis/valkey (#181)
- Rename frostmoln_floating_ip -> frostmoln_public_ip (+ MoveState, LB/k8s/db attrs) (#183)

## [0.14.0] - 2026-07-05

### 🚀 Features

- *(workload-identity)* Add frostmoln_workload_identity_binding resource (#174)

### 🐛 Bug Fixes

- *(api_key)* Normalize expires_at and fix create-response round-trip (#171)
- *(read-replica)* Retry replica delete on transient 409 during primary resize (#177)

### 📚 Documentation

- *(api_key)* Note 2y max expiry in expires_at description (#173)

## [0.13.1] - 2026-07-04

### ⚙️ Miscellaneous Tasks

- Drop internal IdP product name from provider comments + changelog (#169)

## [0.13.0] - 2026-07-04

### 🚀 Features

- *(webserver)* Webserver_deployment resource + public/public_ip (ADR-0097) (#167)

## [0.12.0] - 2026-07-04

### 🚀 Features

- *(kubernetes_cluster)* Scheme attribute — public|internal endpoint exposure (create-only) (#165)

## [0.11.0] - 2026-07-03

### 🚀 Features

- *(instance)* Optional instance_access bool for in-guest agent enrollment (#153)
- *(managed-db)* Resize storage_gb via online /resize; guard shrink and flavor changes (#155)
- *(dns)* Add zone tags to frostmoln_dns_zone (#159)
- *(webserver)* Route storage_gb changes to /resize + guard flavor_id (#163)

### 🐛 Bug Fixes

- *(instance)* Adopt backend-picked availability zone (Optional+Computed) (#157)
- *(subnet)* Wait for delete operation to prevent stale-id on replace (#161)

### 🧪 Testing

- *(kubernetes)* TF_ACC acceptance tests for cluster + node pool (Ph5c phase 5) (#151)

## [0.10.0] - 2026-07-02

### 🚀 Features

- *(cache)* Terraform storage resize for redis/valkey; flavor RequiresReplace (#143)
- *(kubernetes)* Managed-K8s cluster resource + catalog data sources (Ph5c phases 1+2) (#145)
- *(kubernetes)* Cluster addons attribute + frostmoln_kubernetes_addons data source (#147)
- *(kubernetes)* Frostmoln_kubernetes_node_pool resource (Ph5c phase 3) (#149)

## [0.9.0] - 2026-07-01

### 🚀 Features

- *(compute)* Add frostmoln_instance_port_security_groups resource (#139)
- *(read-replica)* Add flavor_id to postgres/mysql read replica resources (#141)

## [0.8.1] - 2026-07-01

### 🐛 Bug Fixes

- *(provider)* Detect instance security-group drift via authoritative GET (#137)

## [0.8.0] - 2026-06-30

### 🚀 Features

- *(provider)* Frostmoln_volume_tiers data source (#135)

## [0.7.0] - 2026-06-30

### 🚀 Features

- Tolerate async 202 operation on managed-service create (#131)

### 🐛 Bug Fixes

- *(provider)* Retry transient OIDC bearer refresh failures once (#132)

## [0.6.0] - 2026-06-30

### 🐛 Bug Fixes

- *(provider)* [**breaking**] Normalize flavor to flavor_id on db/web managed-service resources (#127)

### ⚙️ Miscellaneous Tasks

- Keep provider on minor bumps for breaking changes pre-1.0 (#129)

## [0.5.0] - 2026-06-30

### 🚀 Features

- *(instance)* In-place security_groups update (replace semantics) (#125)

### 📚 Documentation

- Managed-service version/config + engine-specific resource convention (#123)

## [0.4.5] - 2026-06-30

### 🐛 Bug Fixes

- *(provider)* Operate ssh_key by name so import/destroy work (#121)

## [0.4.4] - 2026-06-30

### 🐛 Bug Fixes

- *(provider)* Preserve null description on volume/snapshot read-back (#119)

## [0.4.3] - 2026-06-30

### 🐛 Bug Fixes

- *(provider)* Filter reserved metadata from tags read-back (volume, snapshot, instance datasource) (#117)

## [0.4.2] - 2026-06-30

### 🐛 Bug Fixes

- *(instance)* Filter reserved frostmoln_* metadata out of tags read-back (#115)

## [0.4.1] - 2026-06-30

### 🐛 Bug Fixes

- *(instance)* Preserve security_groups from state in fromAPI (#113)

### 📚 Documentation

- *(provider)* Regenerate docs for v0.4.0 schema + add make docs and CI drift gate (#111)

## [0.4.0] - 2026-06-30

### 🐛 Bug Fixes

- *(provider)* [**breaking**] Align resource/datasource types with backend API contracts (#109)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency frostmoln/workflows to v0.5.0 (#107)

## [0.3.0] - 2026-06-29

### 🚀 Features

- *(database)* Backup_retention_days [35,90] validator + docs (ADR-0085) (#89)
- Drop generic cache resource, normalize version/config attrs (#91)
- Authenticate from an fm CLI session (api_key + OIDC bearer w/ refresh) (#93)
- Shared oidc module + X-FM-Provider-Version gate (#95)
- *(provider)* Provider-level tenant_id to select the operating tenant (#97)
- Adopt oidc v0.3.0 — re-login on dead refresh token + identifying User-Agent (#99)

### 🐛 Bug Fixes

- *(network)* Align VPC/subnet wire tags to contract, drop vestigial region (#101)
- *(network)* Remove unbacked subnet is_public attribute (#103)
- Harden provider OIDC refresh against Zitadel reuse-detection (#105)

## [0.2.0] - 2026-06-28

### 🚀 Features

- 202-tolerant frostmoln_messaging_instance create (#87)

### ⚙️ Miscellaneous Tasks

- Drop dead gitea:3000 insteadOf + actions/setup-go (#83)
- *(deps)* Update pre-commit hook alessandrojcm/commitlint-pre-commit-hook to v9.26.0 (#85)

## [0.1.0] - 2026-06-27

### 🚀 Features

- Initial terraform provider for frostmoln cloud platform
- Filter generated code from coverage reports
- Add flavor versioning attributes and deprecation diagnostics
- Add Terraform resources for managed PostgreSQL
- Add MySQL instance, backup, and read replica resources (#16)
- Add apache, nginx, and webserver domain resources (#25)
- Add cache and valkey Terraform resources (#26)
- Add frostmoln_secret resource and data source (#36)
- Add Scale Groups with launch templates and reconciler (#38)
- *(lb)* L2-1e Terraform load-balancer resources (#48)
- Add frostmoln_regions data source (ADR-0022) (#51)
- *(s3_credential)* Expose per-credential scoping (allowed_buckets/actions/ip_whitelist) (#52)
- *(load_balancer)* Add scheme + floating_ip_id/floating_ip_address attributes (#56)
- *(lb_pool)* Accept source_ip_port algorithm (#57)
- *(terraform)* Nest frostmoln_snapshot under its volume (ADR-0065) (#70)
- Frostmoln_messaging_instance resource + data source (#76)
- Frostmoln_dns_zone + frostmoln_dns_record resources + data source (#77)
- *(instance)* Optional console_password attribute (#78)

### 🐛 Bug Fixes

- Use tenant-scoped paths for SSH key resources
- Add error handling to acceptance test setup workflow
- Apply go fmt formatting
- Use correct API base URL for acceptance tests
- Handle 202 Accepted responses for volume operations
- Remove no-commit-to-branch hook that fails in CI (#17)
- *(deps)* Update hashicorp (#23)
- Align struct field indentation in Redis tests (#24)
- Correct SSH key list JSON tag to match API response (#29)
- Add git.sm.internal to GOINSECURE in CI workflows (#35)
- Rename ec2 health check type to instance (#40)
- Use explicit scopes for API key in acceptance tests (#41)
- *(deps)* Update module github.com/hashicorp/terraform-plugin-docs to v0.25.0 (#43)
- *(deps)* Update module github.com/hashicorp/terraform-plugin-testing to v1.16.0 (#44)
- *(terraform)* Use availabilityZone on the API wire (send + read) (#69)
- *(terraform)* Frostmoln_volume create/resize/delete handle the async Operation envelope (#71)
- *(terraform)* Instance + scale_group create handle the async Operation envelope (#72)
- *(terraform)* Network resource creates handle the async Operation envelope (#73)
- *(terraform)* Load-balancer child creates handle the async Operation envelope (#74)
- *(terraform)* Floating_ip allocate+associate handle the async Operation envelope (#75)

### 🚜 Refactor

- Rename resource prefix fm_ to frostmoln_ and generate docs
- Use go-test-coverage tool output for all coverage reporting
- Rename nlctl CLI references to fm (#28)
- Rebrand NordicLight to Frostmoln / Svenska Moln AB (#30)
- Migrate to vanity Go module path and frostmoln registry (#31)

### 📚 Documentation

- *(region)* Rename eu-north-1->sweden (resource docs/examples/tests) (#47)
- *(flavors)* Versioned flavor IDs in resource examples (ADR-0016) (#49)
- Regenerate provider documentation (tfplugindocs) — add missing resources (#68)

### 🎨 Styling

- Fix go fmt formatting in test files

### 🧪 Testing

- Add acceptance tests and nightly CI workflow
- Achieve 83% coverage with comprehensive unit tests
- *(regions)* Use deployed sweden/falkenberg in fixtures, not fake regions (ADR-0022 Ph3) (#50)
- *(apikey)* Use fmk_ API key prefix in fixtures (was nlak_) (#53)
- Raise terraform-provider coverage to 85% (#60)

### ⚙️ Miscellaneous Tasks

- *(deps)* Update dependency python to 3.14 (#2)
- *(deps)* Update actions/setup-python action to v6
- Gitignore .claude/settings.local.json
- Group hashicorp dependencies in renovate config (#21)
- Update Go to 1.26.1 (#27)
- Stop extending renovate-config preset (#32)
- Remove git.nl.cloud and NordicLight references (#33)
- Rename nordiclight to frostmoln in docs and specs (#34)
- Skip redundant test/lint on push to main (#37)
- Remove proxmox lab traces (#42)
- *(deps)* Update pre-commit hook alessandrojcm/commitlint-pre-commit-hook to v9.25.0 (#45)
- Disable setup-go cache to fix tar race in shared /go-cache (#46)
- Replace NordicLight WIP-name leftovers with Frostmoln (#54)
- Remove unused Makefile (#55)
- *(pre-commit)* Adopt shared workflows/pre-commit.yml@v0.3.1 caller (#58)
- *(deps)* Update dependency frostmoln/workflows to v0.3.2 (#59)
- *(deps)* Update actions/checkout action to v7 (#61)
- *(deps)* Update dependency frostmoln/workflows to v0.3.3 (#62)
- *(deps)* Update dependency frostmoln/workflows to v0.4.0 (#63)
- Pin go-test-coverage to v2.18.8 (kill @latest flaky CI) (#64)
- *(deps)* Update dependency frostmoln/workflows to v0.4.1 (#65)
- *(deps)* Update dependency frostmoln/workflows to v0.4.2 (#66)
- *(deps)* Update dependency frostmoln/workflows to v0.4.3 (#67)
- Publish provider to public Terraform Registry via GoReleaser (#79)
- Add cliff.toml (initial_tag v0.1.0) so first tag is v-prefixed (#81)

<!-- generated by git-cliff -->
