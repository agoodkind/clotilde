.PHONY: help build test test-watch coverage clean fmt lint staticcheck deadcode govulncheck audit \
        install-build-guard uninstall-build-guard setup-hooks \
        deploy install-launch-agent uninstall-launch-agent install-hook uninstall-hook \
        sign notarize

# Optional local overrides (signing creds, never committed). Copy config.mk.example.
-include config.mk

# ---------------------------------------------------------------------------
# Version / build metadata
# ---------------------------------------------------------------------------

BASE_VERSION := $(shell cat VERSION 2>/dev/null || echo "0.0.0")
GIT_TAG      := $(shell git describe --exact-match --tags 2>/dev/null)
COMMIT       := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_DIRTY    := $(shell git diff --quiet && echo false || echo true)
DATE         := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GKLOG_VPKG   := goodkind.io/gklog/version

# Use the exact git tag when present; otherwise stamp with -dev+timestamp.
ifeq ($(GIT_TAG),)
VERSION := $(BASE_VERSION)-dev+$(shell date -u +"%Y%m%d%H%M%S")
else
VERSION := $(patsubst v%,%,$(GIT_TAG))
endif

LDFLAGS := -X '$(GKLOG_VPKG).Commit=$(COMMIT)' \
           -X '$(GKLOG_VPKG).Dirty=$(GIT_DIRTY)' \
           -X '$(GKLOG_VPKG).BuildTime=$(DATE)' \
           -X '$(GKLOG_VPKG).BinHash='

GO_SRC := $(shell find . -name '*.go' -not -path './vendor/*')

# ---------------------------------------------------------------------------
# macOS install paths
# ---------------------------------------------------------------------------

LAUNCH_AGENT_LABEL    := io.goodkind.clyde.daemon
LAUNCH_AGENT_PLIST    := $(HOME)/Library/LaunchAgents/$(LAUNCH_AGENT_LABEL).plist
LAUNCH_AGENT_TEMPLATE := packaging/macos/io.goodkind.clyde.daemon.plist.in
DAEMON_LOG            := $(HOME)/Library/Logs/clyde-daemon.log
CLYDE_BIN             := $(HOME)/.local/bin/clyde
CLYDE_DAEMON_BIN      := $(CURDIR)/dist/clyde
UID                   := $(shell id -u)

# ---------------------------------------------------------------------------
# Default
# ---------------------------------------------------------------------------

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

build: dist/clyde ## Build the clyde binary

dist/clyde: $(GO_SRC) go.mod go.sum
	@echo "Building clyde..."
	@mkdir -p dist
	@go build -ldflags "$(LDFLAGS)" -o dist/clyde ./cmd/clyde
	@echo "✓ Built to dist/clyde"

clean: ## Remove build artifacts
	@echo "Cleaning..."
	@rm -rf dist/
	@rm -f *.test *.out coverage.txt coverage.html
	@find . -name "*.test" -delete
	@echo "✓ Cleaned"

# ---------------------------------------------------------------------------
# Test
# ---------------------------------------------------------------------------

test: ## Run tests with Ginkgo
	@go run github.com/onsi/ginkgo/v2/ginkgo -r --randomize-all --randomize-suites --fail-on-pending --race

test-watch: ## Run tests in watch mode
	@echo "Starting test watch mode..."
	@go run github.com/onsi/ginkgo/v2/ginkgo watch -r

coverage: ## Generate coverage report
	@echo "Generating coverage report..."
	@go run github.com/onsi/ginkgo/v2/ginkgo -r --randomize-all --randomize-suites --cover --coverprofile=coverage.txt
	@go tool cover -html=coverage.txt -o coverage.html
	@echo "✓ Coverage report generated: coverage.html"

# ---------------------------------------------------------------------------
# Code quality
# ---------------------------------------------------------------------------

fmt: ## Format code with gofumpt and goimports
	@echo "Formatting code..."
	@go tool golangci-lint fmt ./...
	@echo "✓ Formatted"

lint: ## Run golangci-lint
	@echo "Running linter..."
	@go tool golangci-lint run ./... && echo "✓ Lint passed"

staticcheck: ## Run Clyde's staticcheck bundle and custom architecture analyzers
	@echo "Running Clyde staticcheck..."
	@go tool clyde-staticcheck ./... && echo "✓ Staticcheck passed"

# CLYDE-125 MITM capture targets. Each target spawns the named
# upstream through the local mitm proxy and writes a JSONL
# transcript under ~/.local/state/clyde/mitm/<upstream>/<timestamp>/.
# Run mitmproxy once first to seed ~/.mitmproxy/mitmproxy-ca-cert.pem.
capture-codex-cli: build
	@dist/clyde mitm capture --upstream codex-cli

capture-codex-desktop: build
	@dist/clyde mitm capture --upstream codex-desktop

capture-claude-code: build
	@dist/clyde mitm capture --upstream claude-code

capture-claude-desktop: build
	@dist/clyde mitm capture --upstream claude-desktop

# wire-snapshot-check diffs every committed reference snapshot
# under research/<upstream>/snapshots/latest/reference.toml against
# the live capture. Fails when any upstream has drifted. Run this
# in CI after capturing fresh transcripts.
wire-snapshot-check: build
	@for upstream in codex-cli codex-desktop claude-code claude-desktop; do \
		ref="research/$$upstream/snapshots/latest/reference.toml"; \
		[ -f "$$ref" ] || continue; \
		live="$$(ls -t ~/.local/state/clyde/mitm/$$upstream 2>/dev/null | head -1)"; \
		if [ -z "$$live" ]; then \
			echo "no live capture for $$upstream; skipping"; \
			continue; \
		fi; \
		live_transcript="$$HOME/.local/state/clyde/mitm/$$upstream/$$live/capture.jsonl"; \
		live_ref="$$HOME/.local/state/clyde/mitm/$$upstream/$$live/reference.toml"; \
		dist/clyde mitm snapshot --upstream $$upstream --output-dir "$$HOME/.local/state/clyde/mitm/$$upstream/$$live" "$$live_transcript" >/dev/null && \
		dist/clyde mitm diff "$$ref" "$$live_ref" || exit 1; \
	done
	@echo "✓ Wire snapshot parity clean"

deadcode: build ## Check for unreachable functions
	@if ! output=$$(go tool deadcode ./...); then \
		echo "go tool deadcode failed"; \
		exit 1; \
	fi; \
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
	@go tool gocyclo -over 15 .
	@echo ""
	@echo "=== Vulnerability check ==="
	@go tool govulncheck ./... || true

install: build ## Install the clyde binary to ~/.local/bin
	@mkdir -p "$(HOME)/.local/bin"
	@cp dist/clyde "$(CLYDE_BIN)"
	@echo "✓ Installed to $(CLYDE_BIN)"

install-build-guard: ## Enforce repo staticcheck on direct go build via GOFLAGS toolexec
	@./scripts/install-go-build-guard.sh

uninstall-build-guard: ## Remove the repo staticcheck toolexec from GOFLAGS
	@./scripts/install-go-build-guard.sh --uninstall

# ---------------------------------------------------------------------------
# Install / deploy (macOS)
# ---------------------------------------------------------------------------

setup-hooks: ## Configure git hooks
	@git config core.hooksPath .githooks
	@chmod +x .githooks/*
	@echo "✓ Git hooks configured"

deploy: build ## Install/start the daemon if needed; otherwise hand it off to the new binary
	@if [ "$$(uname)" != "Darwin" ]; then \
		echo "deploy currently manages the macOS LaunchAgent; use dist/clyde daemon reload on this platform"; \
		exit 1; \
	fi
	@if [ ! -f "$(LAUNCH_AGENT_PLIST)" ] || ! launchctl list "$(LAUNCH_AGENT_LABEL)" >/dev/null 2>&1 || ! launchctl list "$(LAUNCH_AGENT_LABEL)" 2>/dev/null | grep -q '"PID" = [0-9]'; then \
		$(MAKE) install-launch-agent; \
	else \
		dist/clyde daemon reload; \
	fi

install-launch-agent: ## Render and install the daemon LaunchAgent (runs OAuth refresh + adapter + prune in-process)
	@mkdir -p "$(HOME)/Library/LaunchAgents" "$(HOME)/Library/Logs"
	@touch "$(DAEMON_LOG)"
	@sed -e 's|@@CLYDE_DAEMON_BIN@@|$(CLYDE_DAEMON_BIN)|g' \
	     -e 's|@@HOME@@|$(HOME)|g' \
	     -e 's|@@LOG_PATH@@|$(DAEMON_LOG)|g' \
	     "$(LAUNCH_AGENT_TEMPLATE)" > "$(LAUNCH_AGENT_PLIST)"
	@launchctl bootout gui/$(UID)/$(LAUNCH_AGENT_LABEL) 2>/dev/null; true
	@launchctl bootstrap gui/$(UID) "$(LAUNCH_AGENT_PLIST)"
	@echo "✓ LaunchAgent installed: $(LAUNCH_AGENT_PLIST)"
	@echo "  Logs: $(DAEMON_LOG)"

uninstall-launch-agent: ## Remove the clyde daemon LaunchAgent
	@launchctl bootout gui/$(UID)/$(LAUNCH_AGENT_LABEL) 2>/dev/null; true
	@rm -f "$(LAUNCH_AGENT_PLIST)"
	@echo "✓ LaunchAgent removed"

install-hook: ## Register the SessionStart hook in ~/.claude/settings.json
	@mkdir -p "$(HOME)/.claude"
	@touch "$(HOME)/.claude/settings.json"
	@if [ ! -s "$(HOME)/.claude/settings.json" ]; then echo '{}' > "$(HOME)/.claude/settings.json"; fi
	@cp "$(HOME)/.claude/settings.json" "$(HOME)/.claude/settings.json.bak.$$(date +%s)"
	@jq --arg cmd "$(CLYDE_BIN) hook sessionstart" \
		'.hooks = (.hooks // {}) | .hooks.SessionStart = (.hooks.SessionStart // []) | \
		 .hooks.SessionStart = (.hooks.SessionStart | map(select(.hooks[0].command != $$cmd))) + \
		 [{matcher: "*", hooks: [{type: "command", command: $$cmd}]}]' \
		"$(HOME)/.claude/settings.json" > "$(HOME)/.claude/settings.json.tmp"
	@mv "$(HOME)/.claude/settings.json.tmp" "$(HOME)/.claude/settings.json"
	@echo "✓ SessionStart hook registered in ~/.claude/settings.json"

uninstall-hook: ## Remove the SessionStart hook from ~/.claude/settings.json
	@if [ -f "$(HOME)/.claude/settings.json" ]; then \
		jq --arg cmd "$(CLYDE_BIN) hook sessionstart" \
			'if .hooks.SessionStart then .hooks.SessionStart |= map(select(.hooks[0].command != $$cmd)) else . end' \
			"$(HOME)/.claude/settings.json" > "$(HOME)/.claude/settings.json.tmp" && \
		mv "$(HOME)/.claude/settings.json.tmp" "$(HOME)/.claude/settings.json"; \
		echo "✓ SessionStart hook removed"; \
	fi

# ---------------------------------------------------------------------------
# Distribution (macOS signing)
# ---------------------------------------------------------------------------

ifdef CERT_ID
sign: build ## Sign binary with Developer ID Application certificate
	@echo "Signing dist/clyde..."
	@codesign -s "$(CERT_ID)" -f --options runtime --timestamp dist/clyde
	@echo "✓ Signed dist/clyde"

notarize: sign ## Sign and notarize binary for distribution (requires NOTARY_PROFILE in config.mk)
	@echo "Creating notarization zip..."
	@ditto -c -k --keepParent dist/clyde dist/clyde-notarize.zip
	@echo "Submitting for notarization (waiting)..."
	@xcrun notarytool submit dist/clyde-notarize.zip \
		--keychain-profile "$(NOTARY_PROFILE)" \
		--wait
	@rm dist/clyde-notarize.zip
	@echo "✓ Notarized dist/clyde"
else
sign: build ## Sign binary (requires CERT_ID in config.mk)
	@echo "⚠ CERT_ID not set in config.mk. Skipping code signing."
	@echo "  Copy config.mk.example to config.mk and fill in your Developer ID"

notarize: sign ## Sign and notarize binary (requires config.mk)
	@echo "⚠ CERT_ID not set in config.mk. Skipping notarization."
endif
