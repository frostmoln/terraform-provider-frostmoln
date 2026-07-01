# Changelog

All notable changes to this project will be documented in this file.

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
