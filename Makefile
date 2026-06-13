# terraform-provider-frostmoln Makefile

HOSTNAME = registry.terraform.io
NAMESPACE = frostmoln
NAME = frostmoln
BINARY = terraform-provider-${NAME}
VERSION ?= 0.1.0
OS_ARCH = $(shell go env GOOS)_$(shell go env GOARCH)

# Go variables
GOCMD = go
GOBUILD = $(GOCMD) build
GOTEST = $(GOCMD) test
GOMOD = $(GOCMD) mod
GOFMT = gofmt

# Build directories
BUILD_DIR = build

.PHONY: all build clean test testacc lint fmt deps install generate help

# Default target
all: clean fmt test build

# Build the provider binary
build:
	@echo "Building $(BINARY)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -o $(BUILD_DIR)/$(BINARY) ./cmd/terraform-provider-frostmoln

# Install provider locally for development
install: build
	@echo "Installing provider locally..."
	@mkdir -p ~/.terraform.d/plugins/${HOSTNAME}/${NAMESPACE}/${NAME}/${VERSION}/${OS_ARCH}
	@cp $(BUILD_DIR)/$(BINARY) ~/.terraform.d/plugins/${HOSTNAME}/${NAMESPACE}/${NAME}/${VERSION}/${OS_ARCH}/

# Run unit tests
test:
	@echo "Running tests..."
	$(GOTEST) -v -race -coverprofile=coverage.out ./internal/...

# Run acceptance tests
testacc:
	@echo "Running acceptance tests..."
	TF_ACC=1 $(GOTEST) -v -timeout 30m ./internal/...

# Run linter
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping..."; \
	fi

# Format code
fmt:
	@echo "Formatting code..."
	$(GOFMT) -s -w .

# Check formatting
fmt-check:
	@echo "Checking code formatting..."
	@test -z "$$($(GOFMT) -s -l . | tee /dev/stderr)" || (echo "Code is not formatted" && exit 1)

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

# Generate documentation
TFGEN_DIR = /tmp/tfgen-frostmoln
generate: install
	@echo "Generating documentation..."
	@rm -rf $(TFGEN_DIR) && mkdir -p $(TFGEN_DIR)
	@printf 'terraform {\n  required_providers {\n    frostmoln = {\n      source = "$(HOSTNAME)/$(NAMESPACE)/$(NAME)"\n    }\n  }\n}\n' > $(TFGEN_DIR)/main.tf
	@cd $(TFGEN_DIR) && terraform init -plugin-dir=$$HOME/.terraform.d/plugins > /dev/null 2>&1
	@cd $(TFGEN_DIR) && terraform providers schema -json > $(TFGEN_DIR)/raw-schema.json
	@jq '(.provider_schemas["registry.terraform.io/hashicorp/frostmoln"] = .provider_schemas["registry.terraform.io/frostmoln/frostmoln"]) | del(.provider_schemas["registry.terraform.io/frostmoln/frostmoln"])' $(TFGEN_DIR)/raw-schema.json > $(TFGEN_DIR)/schema.json
	$(GOCMD) run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs generate \
		-provider-name frostmoln \
		-providers-schema $(TFGEN_DIR)/schema.json \
		-rendered-provider-name "Frostmoln"
	@rm -rf $(TFGEN_DIR)

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html

# Show help
help:
	@echo "Terraform Provider Frostmoln - Makefile targets:"
	@echo ""
	@echo "Development:"
	@echo "  make              Build with fmt and tests"
	@echo "  make build        Build provider binary"
	@echo "  make test         Run unit tests"
	@echo "  make testacc      Run acceptance tests (requires TF_ACC=1)"
	@echo "  make lint         Run linter"
	@echo "  make fmt          Format code"
	@echo "  make deps         Download and tidy dependencies"
	@echo ""
	@echo "Installation:"
	@echo "  make install      Install provider locally for development"
	@echo ""
	@echo "Documentation:"
	@echo "  make generate     Generate provider documentation"
	@echo ""
	@echo "Other:"
	@echo "  make clean        Clean build artifacts"
	@echo "  make help         Show this help"
