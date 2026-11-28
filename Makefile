# Sumi — single entry point for every local workflow.
# Run `make help` for the full target list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# ---- Tunables (override on the command line, e.g. make dev PORT=9000) ----
PORT        ?= 8087
WEB_PORT    ?= 5173
DATA_DIR    ?= $(HOME)/.sumi
DB          ?= $(DATA_DIR)/sumi.db
SERVE_URL   ?= http://127.0.0.1:$(PORT)
BIN             ?= bin/sumi
DESKTOP_BIN     ?= bin/Sumi.app
INSTALL_APP_DIR ?= /Applications
GO              ?= go
NODE_PKG    ?= pnpm
# Single source of truth for the release version. Release automation overrides this,
# and `make build VERSION=6.0.5` produces an identically stamped binary locally.
VERSION         ?= 6.0.5
LDFLAGS         := -X github.com/abcdlsj/sumi/internal/version.Version=$(VERSION)

# Pairing inputs for `make pair`: EMAIL=... PASSWORD=... [SPACE=studio] [DESKTOP_CONFIG=...]
EMAIL             ?=
PASSWORD          ?=
SPACE             ?= studio
DESKTOP_CONFIG    ?= $(HOME)/.sumi/desktop.json

.PHONY: help
help: ## Show every target
	@printf 'Sumi targets:\n\n'
	@grep -hE '^[a-zA-Z0-9_.-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-18s\033[0m %s\n", $$1, $$2}'
	@printf '\nOverrides: PORT=%s DATA_DIR=%s SERVE_URL=%s VERSION=%s\n' '$(PORT)' '$(DATA_DIR)' '$(SERVE_URL)' '$(VERSION)'
	@printf 'Pairing:   make pair EMAIL=you@example.com PASSWORD=secret\n'

# ---- Environment ----

.PHONY: install
install: ## Install Web and Go dependencies
	$(NODE_PKG) --dir web install
	$(GO) mod download
	cd desktop && $(GO) mod download

.PHONY: doctor
doctor: ## Verify the local toolchain (Go, Node, Wails v3)
	@$(GO) version
	@node --version
	@$(NODE_PKG) --version
	@command -v wails3 >/dev/null || command -v wails >/dev/null || \
		{ echo "wails3 CLI missing: go install github.com/wailsapp/wails/v3/cmd/wails3@latest"; exit 1; }
	@echo "toolchain ok"

# ---- Build ----

.PHONY: icons
icons: ## Regenerate app icon SVGs, desktop PNG/ICNS, and Web favicon
	node assets/icon/generate.mjs

.PHONY: web
web: ## Build the Web UI into web/dist
	$(NODE_PKG) --dir web build

.PHONY: web-dev
web-dev: ## Run the Vite dev server (proxies /api to SERVE_URL)
	$(NODE_PKG) --dir web dev

.PHONY: web-assets
web-assets: web web-embed ## Build the Web UI and embed it for go:embed

.PHONY: web-embed
web-embed: ## Embed an already built web/dist (used by CI after a cached build)
	@rm -rf internal/webui/assets/dist
	@mkdir -p internal/webui/assets
	@cp -R web/dist internal/webui/assets/dist

.PHONY: bin
bin: ## Build the sumi binary into bin/sumi
	@mkdir -p bin
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sumi

.PHONY: build
build: web-assets bin ## Build Web UI and the sumi binary with embedded UI

.PHONY: desktop
# Wails keeps its own module under desktop/, so the shell builds independently of the server module.
# Stage into a fresh bundle and ad-hoc sign before swapping so running instances never hit
# macOS Taskgated Invalid Signature from in-place Mach-O overwrites.
DESKTOP_GOARCH ?= $(shell $(GO) env GOARCH)
desktop: web-assets ## Build the Wails v3 desktop shell into bin/Sumi.app
	@mkdir -p bin
	@scripts/package-macos-app.sh --version "$(VERSION)" --goarch $(DESKTOP_GOARCH) --out $(DESKTOP_BIN)

.PHONY: install-app
install-app: desktop ## Install Sumi.app into INSTALL_APP_DIR (default /Applications)
	@mkdir -p "$(INSTALL_APP_DIR)"
	@target="$(INSTALL_APP_DIR)/Sumi.app"; \
		staging="$$target.staging"; \
		rm -rf "$$staging"; \
		ditto "$(DESKTOP_BIN)" "$$staging"; \
		rm -rf "$$target"; \
		mv "$$staging" "$$target"
	@echo "installed: $(INSTALL_APP_DIR)/Sumi.app"

.PHONY: all
all: build desktop ## Build everything

# ---- Run ----

.PHONY: pair
pair: bin start ## Pair this computer and write desktop.json (EMAIL= PASSWORD= [SPACE=])
	@test -n "$(EMAIL)" || { echo "EMAIL is required: make pair EMAIL=you@example.com PASSWORD=secret"; exit 1; }
	@test -n "$(PASSWORD)" || { echo "PASSWORD is required: make pair EMAIL=you@example.com PASSWORD=secret"; exit 1; }
	$(BIN) pair --server $(SERVE_URL) --email "$(EMAIL)" --password "$(PASSWORD)" \
		--space "$(SPACE)" --config "$(DESKTOP_CONFIG)" --data-dir "$(DATA_DIR)"

.PHONY: server
server: bin ## Run the Server in the foreground (default :8087)
	$(BIN) server --addr 127.0.0.1:$(PORT) --db $(DB) \
		--web-origin http://127.0.0.1:$(WEB_PORT)

.PHONY: dev
dev: ## Run Server and Vite together for Web development
	@$(MAKE) --no-print-directory -j2 server web-dev

.PHONY: computer
computer: bin ## Run the local Computer daemon standalone (TOKEN=...)
	@test -n "$(TOKEN)" || { echo "TOKEN is required: make computer TOKEN=<computer token>"; exit 1; }
	$(BIN) computer --server $(SERVE_URL) --token "$(TOKEN)" --data-dir $(DATA_DIR)/computer

.PHONY: start
start: bin ## Start the Server in the background (logs in .sumi/server.log)
	@mkdir -p $(DATA_DIR)
	@pgrep -f "$(BIN) server --addr 127.0.0.1:$(PORT)" >/dev/null 2>&1 && { echo "server already running on :$(PORT)"; exit 0; } || true
	@nohup $(BIN) server --addr 127.0.0.1:$(PORT) --db $(DB) \
		> $(DATA_DIR)/server.log 2>&1 & echo $$! > $(DATA_DIR)/server.pid
	@printf 'server starting on %s (log: %s/server.log)\n' '$(SERVE_URL)' '$(DATA_DIR)'
	@for i in $$(seq 1 60); do \
		if curl -fsS -m 1 $(SERVE_URL)/api/v1/health >/dev/null 2>&1; then echo "server ready"; exit 0; fi; \
		sleep 0.25; \
	done; \
	echo "server did not become healthy; see $(DATA_DIR)/server.log"; exit 1

.PHONY: stop
stop: ## Stop the background Server started by `make start`
	@if [ -f $(DATA_DIR)/server.pid ]; then \
		kill $$(cat $(DATA_DIR)/server.pid) 2>/dev/null || true; \
		rm -f $(DATA_DIR)/server.pid; \
		echo "server stopped"; \
	else \
		echo "no background server pid file"; \
	fi

.PHONY: upgrade
upgrade: build ## Build and hot-upgrade running local Server and Computer without stopping Agents
	$(BIN) upgrade --binary $(BIN) --data-root $(DATA_DIR)

.PHONY: app
app: desktop start ## Build everything and launch the desktop shell
	@if [ ! -f "$(DESKTOP_CONFIG)" ]; then \
		echo "no desktop config at $(DESKTOP_CONFIG)"; \
		echo "run: make pair EMAIL=you@example.com PASSWORD=secret"; \
	fi
	@open $(DESKTOP_BIN)
	@echo "Sumi launched (tray icon); 'make stop' stops the background server"

.PHONY: run
run: app ## Alias for `make app`

# ---- Verify ----

.PHONY: fmt
fmt: ## Format Go sources
	$(GO) fmt ./...

.PHONY: lint
lint: ## Run the repo's required static checks
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	$(GO) vet ./...
	cd desktop && $(GO) vet ./...
	$(NODE_PKG) --dir web typecheck

.PHONY: test
test: ## Run Go and Web tests
	$(GO) test ./...
	cd desktop && $(GO) test ./...
	$(NODE_PKG) --dir web test

.PHONY: verify
verify: lint test ## Run every required check

# ---- Housekeeping ----

.PHONY: clean
clean: ## Remove build output
	rm -rf bin web/dist internal/webui/assets/dist $(DESKTOP_BIN)

.PHONY: clean-data
clean-data: ## Remove local SQLite + objects (destructive)
	@read -p "delete $(DATA_DIR)? [y/N] " ans && [ "$$ans" = "y" ] && rm -rf $(DATA_DIR) || echo "aborted"
