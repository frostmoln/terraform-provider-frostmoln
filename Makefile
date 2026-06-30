.PHONY: docs generate

# Regenerate the Terraform provider docs/ from the live provider schema.
# See scripts/gen-docs.sh for why this can't be plain `tfplugindocs generate`.
docs:
	./scripts/gen-docs.sh

# Alias for the `make generate` reference in CLAUDE.md.
generate: docs
