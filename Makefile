.PHONY: help build build-tui-qa tui-qa test test-watch install install-launch-agent install-hook clean lint fmt coverage setup-hooks deadcode govulncheck audit sign notarize uninstall-launch-agent uninstall-hook slog-audit

# Optional local overrides (signing creds, never committed). Copy config.mk.example.
-include config.mk

# Build variables
BASE_VERSION := $(shell cat VERSION 2>/dev/null || echo "0.0.0")
GIT_TAG := $(shell git describe --exact-match --tags 2>/dev/null)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
GIT_DIRTY := $(shell git diff --quiet && echo false || echo true)
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GKLOG_VPKG := goodkind.io/gklog/version
# If building from a git tag, use it. Otherwise append -dev+timestamp
ifeq ($(GIT_TAG),)
	VERSION := $(BASE_VERSION)-dev+$(shell date -u +"%Y%m%d%H%M%S")
else
	VERSION := $(patsubst v%,%,$(GIT_TAG))
endif

LDFLAGS := -X '$(GKLOG_VPKG).Commit=$(COMMIT)' \
           -X '$(GKLOG_VPKG).Dirty=$(GIT_DIRTY)' \
           -X '$(GKLOG_VPKG).BuildTime=$(DATE)' \
           -X '$(GKLOG_VPKG).BinHash='

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

build: ## Build the clyde binary
	@echo "Building clyde..."
	@mkdir -p dist
	@go build -ldflags "$(LDFLAGS)" -o dist/clyde ./cmd/clyde
	@echo "✓ Built to dist/clyde"

build-tui-qa: ## Build the clyde-tui-qa agent harness (tmux, PTY, iTerm drivers)
	@mkdir -p dist
	@go build -ldflags "$(LDFLAGS)" -o dist/clyde-tui-qa ./cmd/clyde-tui-qa
	@echo "✓ Built to dist/clyde-tui-qa"

tui-qa: build build-tui-qa ## Build clyde and clyde-tui-qa (requires tmux for tmux driver; iTerm on macOS)
	@echo "Run interactively: dist/clyde-tui-qa repl --driver tmux --clyde dist/clyde --isolated /tmp/clyde-tuiqa-$$"
	@echo "Prerequisites: tmux on PATH; iTerm2 for --driver iterm (macOS); PTY uses creack/pty and vt10x in-process"

test: ## Run tests with Ginkgo
	@go run github.com/onsi/ginkgo/v2/ginkgo -r --randomize-all --randomize-suites --fail-on-pending --race

test-watch: ## Run tests in watch mode
	@echo "Starting test watch mode..."
	@go run github.com/onsi/ginkgo/v2/ginkgo watch -r

LAUNCH_AGENT_LABEL := io.goodkind.clyde.daemon
LAUNCH_AGENT_PLIST := $(HOME)/Library/LaunchAgents/$(LAUNCH_AGENT_LABEL).plist
LAUNCH_AGENT_TEMPLATE := packaging/macos/io.goodkind.clyde.daemon.plist.in
DAEMON_LOG := $(HOME)/Library/Logs/clyde-daemon.log
CLYDE_BIN := $(HOME)/.local/bin/clyde
UID := $(shell id -u)

install: build ## Install clyde to ~/.local/bin and restart daemon via launchd
	@mkdir -p "$(HOME)/.local/bin"
	@ln -sf "$(CURDIR)/dist/clyde" "$(CLYDE_BIN)"
	@if [ -f "$(LAUNCH_AGENT_PLIST)" ]; then \
		launchctl bootout gui/$(UID)/$(LAUNCH_AGENT_LABEL) 2>/dev/null || true; \
		sleep 1; \
		launchctl bootstrap gui/$(UID) "$(LAUNCH_AGENT_PLIST)" || { echo "launchctl bootstrap failed; daemon NOT running"; exit 1; }; \
		echo "✓ Installed to $(CLYDE_BIN) (daemon restarted via launchd)"; \
	else \
		-pkill -f "clyde daemon" 2>/dev/null; true; \
		echo "✓ Installed to $(CLYDE_BIN) (run 'make install-launch-agent' to register LaunchAgent)"; \
	fi

install-launch-agent: ## Render and install the daemon LaunchAgent (runs OAuth refresh + adapter + prune in-process)
	@mkdir -p "$(HOME)/Library/LaunchAgents" "$(HOME)/Library/Logs"
	@touch "$(DAEMON_LOG)"
	@sed -e 's|@@CLYDE_BIN@@|$(CLYDE_BIN)|g' \
	     -e 's|@@HOME@@|$(HOME)|g' \
	     -e 's|@@LOG_PATH@@|$(DAEMON_LOG)|g' \
	     "$(LAUNCH_AGENT_TEMPLATE)" > "$(LAUNCH_AGENT_PLIST)"
	@launchctl bootout gui/$(UID)/$(LAUNCH_AGENT_LABEL) 2>/dev/null; true
	@launchctl bootstrap gui/$(UID) "$(LAUNCH_AGENT_PLIST)"
	@echo "✓ LaunchAgent installed: $(LAUNCH_AGENT_PLIST)"
	@echo "  Logs: $(DAEMON_LOG)"

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

coverage: ## Generate coverage report
	@echo "Generating coverage report..."
	@go run github.com/onsi/ginkgo/v2/ginkgo -r --randomize-all --randomize-suites --cover --coverprofile=coverage.txt
	@go tool cover -html=coverage.txt -o coverage.html
	@echo "✓ Coverage report generated: coverage.html"

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

slog-audit: ## Fail when banned logging patterns slip into production code (see docs/SLOG.md)
	@./scripts/slog-audit.sh

audit: ## Run complexity and vulnerability checks (informational)
	@echo "=== Cyclomatic complexity (>15) ==="
	@go tool gocyclo -over 15 .
	@echo ""
	@echo "=== Vulnerability check ==="
	@go tool govulncheck ./... || true

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

uninstall-launch-agent: ## Remove the clyde daemon LaunchAgent
	@launchctl bootout gui/$(UID)/$(LAUNCH_AGENT_LABEL) 2>/dev/null; true
	@rm -f "$(LAUNCH_AGENT_PLIST)"
	@echo "✓ LaunchAgent removed"
