#!/usr/bin/env bash
# Regenerate the Terraform provider docs/ from the live provider schema.
#
# Why this dance instead of plain `tfplugindocs generate`? tfplugindocs defaults
# to `go build` at the MODULE ROOT to build the provider, but this provider's
# main lives in cmd/terraform-provider-frostmoln/ (there is no root main.go), so
# the default path fails with "no Go files in <root>". We sidestep it by feeding
# tfplugindocs a schema JSON dumped from the built binary via --providers-schema,
# which makes it skip both the root build and the Terraform CLI call.
#
# Requires: terraform in PATH (the SDK acceptance tests download one in CI; the
# CI docs-drift gate installs a pinned terraform before calling this script).
set -euo pipefail

cd "$(dirname "$0")/.."

command -v terraform >/dev/null || { echo "error: terraform not found in PATH" >&2; exit 1; }
command -v go >/dev/null || { echo "error: go not found in PATH" >&2; exit 1; }

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

# 1. Build the provider from its cmd/ main. The binary MUST be named exactly
#    terraform-provider-frostmoln so the dev_overrides lookup below finds it.
go build -o "$workdir/terraform-provider-frostmoln" ./cmd/terraform-provider-frostmoln

# 2. Dump the provider schema JSON. dev_overrides points Terraform straight at
#    the built binary, so `terraform providers schema` needs no `terraform init`.
#    We register under the hashicorp/ namespace on purpose: the schema bytes are
#    identical regardless of the source address, and tfplugindocs only looks up
#    "<name>" or "registry.terraform.io/hashicorp/<name>" in the schema JSON
#    (terraformProviderSchemaFromFile) — a frostmoln/frostmoln key is never checked.
cat > "$workdir/dev.tfrc" <<EOF
provider_installation {
  dev_overrides { "hashicorp/frostmoln" = "$workdir" }
  direct {}
}
EOF
mkdir -p "$workdir/schema"
cat > "$workdir/schema/main.tf" <<'EOF'
terraform {
  required_providers {
    frostmoln = { source = "hashicorp/frostmoln" }
  }
}
EOF
TF_CLI_CONFIG_FILE="$workdir/dev.tfrc" \
  terraform -chdir="$workdir/schema" providers schema -json > "$workdir/schema.json"

# 3. Render docs/ from the schema (skips the broken root build).
go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate \
  --providers-schema "$workdir/schema.json" \
  --provider-name frostmoln \
  --rendered-provider-name Frostmoln

echo "docs/ regenerated from provider schema."
