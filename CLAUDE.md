# terraform-provider-frostmoln Development Guide

> See [shared project context](../claude-config/CLAUDE.md) for platform-wide guidelines.

Module: `go.frostmoln.internal/terraform-provider-frostmoln`

Terraform provider `frostmoln` (resources `frostmoln_*`), built on terraform-plugin-framework,
talking to api-gateway. Review rules: `REVIEW.md`.

## Layout

```
cmd/terraform-provider-frostmoln/   entry point
internal/provider/                  schema, Configure, registration, provider-wide contract tests
internal/client/                    HTTP client, auth, error envelopes, poller
internal/resource/<name>/           one package per resource (resource.go, model.go)
internal/datasource/<name>/         one package per data source
internal/scopedecl/ idform/ pubid/ planmod/ stateupgrade/   shared schema machinery
examples/  templates/  docs/        HCL examples, tfplugindocs templates, generated docs
```

## Commands

```bash
go build ./... && go test ./...
TF_ACC=1 go test -v -timeout 30m ./internal/...   # acceptance, real API
make generate                                     # docs/ via scripts/gen-docs.sh (not plain tfplugindocs)
```

A new resource also needs: registration in `provider.go`, a `scopedecl` declaration (below), an
`examples/resources/frostmoln_<name>/resource.tf`.

OIDC refresh for the fm-session credential source comes from the shared `go.frostmoln.internal/oidc`
module ([oidc/CLAUDE.md](../oidc/CLAUDE.md)); never a hand copy. The provider stamps
`X-FM-Provider-Version` and handles the 426 via the shared `oidc` `VersionGate` /
`NewUpgradeRequiredError`; gateway side: api-gateway `CLAUDE.md` "Client minimum-version gate".

## Release and distribution

Governing decision: ADR-0079 (the Terraform Registry is its one byte-host exception).

- Canonical source, review and CI stay on `git.sm.internal`. `.gitea/workflows/release.yml` cuts
  `vX.Y.Z`; the tag-triggered `goreleaser.yml` publishes to `registry.terraform.io/frostmoln/frostmoln`.
- The public GitHub repo `Frostmoln/terraform-provider-frostmoln` is a full-source mirror pushed at
  release; no PRs land there. The registry reads `docs/`, `LICENSE`,
  `terraform-registry-manifest.json` from the tag and signed assets from the GitHub Release.
- **Signing is GPG, not minisign** (the registry accepts only GPG): passphrase-less key in
  `GPG_PRIVATE_KEY_B64`. Never rotated without operator approval.
- Pushes use a per-run `frostmoln-goreleaser` GitHub App token; never a PAT or deploy key.
- MPL-2.0; manifest declares protocol 6.0. **`enforce_siv_path` stays off** in `release.yml`: the
  provider is a leaf plugin binary, never an imported module.

## Plan modifier order: `UseStateForUnknown()` before `RequiresReplace()`

On an **Optional+Computed** attribute, `UseStateForUnknown()` goes FIRST. The framework marks a
null-config Computed attribute unknown on update; reversed, `RequiresReplace` compares unknown
against state and plans a destroy/recreate on any unrelated change. Enforced by
`internal/provider/planmodifier_order_test.go`; a new create-only Optional+Computed attribute goes
in its `mustReplaceOnRealChange` table.

- A `RequiresReplaceIf` whose predicate reads `ConfigValue` cannot be held by that table
  (`replayModifiers` passes a null config); `TestScopeDeclarations` covers it instead
  (`frostmoln_postgres_instance` `restore_from`). Do not add it to the table.
- Exception: `frostmoln_secret`'s `content_type`, `max_versions`, `recovery_window_days` are
  create-only but must NOT replace: a soft-deleted secret keeps its name for up to 30 days, so the
  recreate 409s. They use `planmod.*UseStateOrDefault` (never a schema `Default`, which overrides
  an imported value) plus a `ModifyPlan` refusal. Copy this for anything not recreatable by name.
- Exempt: attributes with a schema `Default`, and create-time behaviour flags with a `Default`
  and no `RequiresReplace` (`frostmoln_security_group` `delete_default_egress`, warned in `ModifyPlan`).

## Surface Contract doctrine

A parent resource owns each child collection authoritatively; members are separate per-id
resources. Never inline a child collection into a parent schema or mix both shapes. References
point at computed attributes, never literal addresses. Every platform-invented default has an
explicit policy (delete-on-create / adopt / keep-with-docs). Full doctrine:
`templates/guides/surface-contract.md.tmpl`. `TestSurfaceContract` is default-deny over
`allowedInlineCollections`.

## Authoritative-scope declarations

Every resource has an entry in `internal/scopedecl/declarations.go` (create-immutable fields WITH a
reason, platform-mutable computed fields, `ImmutableWithoutReplace` refusals, default policies) and
its `Description` carries `scopedecl.Summary("frostmoln_<name>")`. `TestScopeDeclarations` checks
both directions. A new resource ships only with its declaration.

## Managed-service instance resource conventions

- **"Engine" is retired; a managed offer's implementations are TYPES** (only the Application
  Gateway's WAF inspection engine keeps the word). `no_engine_vocabulary_test.go` enforces it.
- Type-specific resources (`frostmoln_redis_instance`, ...), never a generic `*_instance`.
- Bare `version` (wire `typeVersion`), `config` Map of String (wire `typeConfig`), `flavor_id`
  (wire `flavorId`) everywhere.
- **Renaming an HCL attribute requires a schema `Version` bump and an upgrader**
  (`stateupgrade.RenameStringAttr`); without it a Default-bearing attribute plans a DESTROY.
- **Removing one also bumps `Version`**, and every older upgrader drops it too. Prove it with a
  literal prior-version state row through the real RPCs at the WIRE version (idform adds one):
  `internal/resource/load_balancer/upgrade_rpc_test.go`.

## Policy documents: FRN read-back fold

`frostmoln_iam_policy` compares configured vs stored documents via `sameServerDoc`
(`frn_compare.go`), which undoes exactly identity's canonical-FRN rewrite and nothing more.
`internal/resource/iam_policy/iam_nouns.json` changes in lockstep with identity's
`TestNormaliseFRN_NounsMatchTheTerraformProviderTable`. Never widen the fold to a name-keyed noun or
another prefix (it would hide drift). Config-to-config comparisons (`onlyTagsDiffer`) stay exact.

## Public ids (ADR-0129): `internal/idform` and `internal/pubid`

The provider requests public ids and keeps them in state. `internal/pubid` copies the servicekit
codec, tested against verbatim copies of servicekit `pubid/testdata/` `vectors.json`/`kinds.json`.
Every resource/data source is registered through `idform.Wrap`/`WrapDataSource`, which adds one
schema version with an id-rewriting upgrader and suppresses spelling-only diffs and replacements.

- **Compare ids with `pubid.Equal`, never `==`.**
- A new resource holding Frostmoln UUID ids gets an `idform.Kinds` entry (`TestIDFormKinds`).
- **Never list an id whose kind depends on a sibling field, or one the API does not encode**: a
  wrong kind plans a REPLACEMENT.
- **Never bump a resource's own `Version` without its own upgrader**; the wrapper adds one on top.

## Provider configuration

`api_endpoint` must include the `/api` suffix (`https://api.frostmoln.cloud/api`, the default);
a bare host 404s at the edge with `NOT_FOUND` during Configure.

## API Endpoints Reference

- Tenant-scoped calls use `/v1/tenants/{t}/...`. The gateway re-scopes the auth context only on that
  path, so a bare `/v1/api-keys` or `/v1/images` silently acts on the caller's HOME tenant (an
  org-invited user has none, and every scope is refused).
- Messaging vhosts/users/permissions: poll the OBJECT, never `/v1/operations`; `ready` with a
  `failureReason` is a FAILED change. All three share the per-instance lock in `internal/messagingobj`.
