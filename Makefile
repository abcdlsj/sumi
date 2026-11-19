# Sumi — single entry point for every local workflow.
# Run `make help` for the full target list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# ---- Tunables (override on the command line, e.g. make dev PORT=9000) ----
PORT        ?= 8087
WEB_PORT    ?= 5173
DATA_DIR    ?= $(HOME)/.sumi/v2
DB          ?= $(DATA_DIR)/sumi.db
SERVE_URL   ?= http://127.0.0.1:$(PORT)
BIN         ?= bin/sumi
DESKTOP_BIN ?= bin/Sumi.app
GO          ?= go
NODE_PKG    ?= pnpm

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
	@printf '\nOverrides: PORT=%s DATA_DIR=%s SERVE_URL=%s\n' '$(PORT)' '$(DATA_DIR)' '$(SERVE_URL)'
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

.PHONY: web
web: ## Build the Web UI into web/dist
	$(NODE_PKG) --dir web build

.PHONY: web-dev
web-dev: ## Run the Vite dev server (proxies /api to SERVE_URL)
	$(NODE_PKG) --dir web dev

.PHONY: bin
bin: ## Build the sumi binary into bin/sumi
	@mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/sumi

.PHONY: build
build: web bin ## Build Web UI and the sumi binary

.PHONY: desktop-assets
desktop-assets: web ## Copy the built Web UI into the desktop shell
	@rm -rf desktop/assets/dist
	@mkdir -p desktop/assets
	@cp -R web/dist desktop/assets/dist
.PHONY: desktop
# Wails keeps its own module under desktop/, so the shell builds independently of the server module.
SHELL_BUILD := cd desktop && CGO_LDFLAGS="-framework UniformTypeIdentifiers" $(GO) build -tags production -trimpath
desktop: desktop-assets ## Build the Wails v3 desktop shell into bin/Sumi.app
	@mkdir -p bin $(DESKTOP_BIN)/Contents/MacOS $(DESKTOP_BIN)/Contents/Resources
	$(SHELL_BUILD) -o ../bin/sumi-desktop .
	@cp bin/sumi-desktop $(DESKTOP_BIN)/Contents/MacOS/Sumi
	@cp desktop/Info.plist $(DESKTOP_BIN)/Contents/Info.plist
	@echo "desktop app: $(DESKTOP_BIN)"

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
	@nohup $(BIN) server --addr 127.0.0.1:$(PORT) --db $(DB) --web-dir web/dist \
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
	@test -z "$$(gofmt -l . | grep -v '^desktop/assets/')" || { gofmt -l .; exit 1; }
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
	rm -rf bin web/dist desktop/assets/dist $(DESKTOP_BIN)

.PHONY: clean-data
clean-data: ## Remove local SQLite + objects (destructive)
	@read -p "delete $(DATA_DIR)? [y/N] " ans && [ "$$ans" = "y" ] && rm -rf $(DATA_DIR) || echo "aborted"
