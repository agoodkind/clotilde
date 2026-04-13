.PHONY: help build test test-watch install clean lint fmt coverage vendor setup-hooks deadcode govulncheck audit sign notarize uninstall-launch-agent

# Optional local overrides (signing creds, never committed — copy config.mk.example)
-include config.mk

# Build variables
BASE_VERSION := $(shell cat VERSION 2>/dev/null || echo "0.0.0")
GIT_TAG := $(shell git describe --exact-match --tags 2>/dev/null)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# If building from a git tag, use it. Otherwise append -dev+timestamp
ifeq ($(GIT_TAG),)
	VERSION := $(BASE_VERSION)-dev+$(shell date -u +"%Y%m%d%H%M%S")
else
	VERSION := $(patsubst v%,%,$(GIT_TAG))
endif

LDFLAGS := -X 'github.com/fgrehm/clotilde/cmd.version=$(VERSION)' \
           -X 'github.com/fgrehm/clotilde/cmd.commit=$(COMMIT)' \
           -X 'github.com/fgrehm/clotilde/cmd.date=$(DATE)'

# Default target
help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

setup-hooks: ## Configure git hooks
	@git config core.hooksPath .githooks
	@chmod +x .githooks/*
	@echo "✓ Git hooks configured"

build: ## Build the clotilde binary
	@echo "Building clotilde..."
	@mkdir -p dist
	@go build -ldflags "$(LDFLAGS)" -o dist/clotilde .
	@echo "✓ Built to dist/clotilde"

test: ## Run tests with Ginkgo
	@go run github.com/onsi/ginkgo/v2/ginkgo -r --randomize-all --randomize-suites --fail-on-pending --race

test-watch: ## Run tests in watch mode
	@echo "Starting test watch mode..."
	@go run github.com/onsi/ginkgo/v2/ginkgo watch -r

LAUNCH_AGENT_LABEL := io.goodkind.clotilde.daemon
LAUNCH_AGENT_PLIST := $(HOME)/Library/LaunchAgents/$(LAUNCH_AGENT_LABEL).plist
UID := $(shell id -u)

install: build ## Install clotilde to ~/.local/bin and restart daemon via launchd
	@mkdir -p "$(HOME)/.local/bin"
	@ln -sf "$(CURDIR)/dist/clotilde" "$(HOME)/.local/bin/clotilde"
	@if [ -f "$(LAUNCH_AGENT_PLIST)" ]; then \
		launchctl bootout gui/$(UID)/$(LAUNCH_AGENT_LABEL) 2>/dev/null; true; \
		launchctl bootstrap gui/$(UID) "$(LAUNCH_AGENT_PLIST)" 2>/dev/null; true; \
		echo "✓ Installed to ~/.local/bin/clotilde (daemon restarted via launchd)"; \
	else \
		-pkill -f "clotilde daemon" 2>/dev/null; true; \
		echo "✓ Installed to ~/.local/bin/clotilde (run 'clotilde setup' to register LaunchAgent)"; \
	fi

clean: ## Remove build artifacts
	@echo "Cleaning..."
	@rm -rf dist/
	@rm -f *.test *.out coverage.txt coverage.html
	@find . -name "*.test" -delete
	@echo "✓ Cleaned"

lint: ## Run golangci-lint
	@echo "Running linter..."
	@go tool golangci-lint run ./... && echo "✓ Lint passed"

fmt: ## Format code with gofumpt and goimports
	@echo "Formatting code..."
	@go tool golangci-lint fmt ./...
	@echo "✓ Formatted"

coverage: ## Generate test coverage report
	@echo "Generating coverage report..."
	@go run github.com/onsi/ginkgo/v2/ginkgo -r --randomize-all --randomize-suites --cover --coverprofile=coverage.txt
	@go tool cover -html=coverage.txt -o coverage.html
	@echo "✓ Coverage report generated: coverage.html"

deadcode: ## Check for unreachable functions
	@output=$$(go tool deadcode ./...) || exit 1; \
	filtered=$$(echo "$$output" | grep -v \
		-e 'cmd/root.go:.*NewRootCmd' \
		-e 'internal/testutil/claude.go:.*CreateFakeClaude' \
		-e 'internal/testutil/claude.go:.*ReadClaudeArgs' \
	|| true); \
	if [ -n "$$filtered" ]; then \
		echo "Dead code found:"; \
		echo "$$filtered"; \
		exit 1; \
	fi
	@echo "✓ No dead code found"

govulncheck: ## Run vulnerability check
	@go tool govulncheck ./...

audit: ## Run complexity and vulnerability checks (informational)
	@echo "=== Cyclomatic complexity (>15) ==="
	@go tool gocyclo -over 15 -ignore 'vendor/' . || true
	@echo ""
	@echo "=== Vulnerability check ==="
	@go tool govulncheck ./... || true

vendor: ## Update vendored dependencies
	@echo "Vendoring dependencies..."
	@go mod tidy
	@go mod vendor
	@echo "✓ Dependencies vendored"

ifdef CERT_ID
sign: build ## Sign binary with Developer ID Application certificate
	@echo "Signing dist/clotilde..."
	@codesign -s "$(CERT_ID)" -f --options runtime --timestamp dist/clotilde
	@echo "✓ Signed dist/clotilde"

notarize: sign ## Sign and notarize binary for distribution (requires NOTARY_PROFILE in config.mk)
	@echo "Creating notarization zip..."
	@ditto -c -k --keepParent dist/clotilde dist/clotilde-notarize.zip
	@echo "Submitting for notarization (waiting)..."
	@xcrun notarytool submit dist/clotilde-notarize.zip \
		--keychain-profile "$(NOTARY_PROFILE)" \
		--wait
	@rm dist/clotilde-notarize.zip
	@echo "✓ Notarized dist/clotilde"
else
sign: build ## Sign binary (requires CERT_ID in config.mk)
	@echo "⚠ CERT_ID not set in config.mk — skipping code signing"
	@echo "  Copy config.mk.example to config.mk and fill in your Developer ID"

notarize: sign ## Sign and notarize binary (requires config.mk)
	@echo "⚠ CERT_ID not set in config.mk — skipping notarization"
endif

uninstall-launch-agent: ## Remove the clotilde daemon LaunchAgent
	@launchctl bootout gui/$(UID)/$(LAUNCH_AGENT_LABEL) 2>/dev/null; true
	@rm -f "$(LAUNCH_AGENT_PLIST)"
	@echo "✓ LaunchAgent removed"
