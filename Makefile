.PHONY: test test-short test-integration test-connection test-azure test-all lint build release-ready doctor build-clean sync-ai diagrams

test:                  ## Run unit tests only (no database, short mode)
	go test -short ./...

test-short:            ## Alias for `test` — kept for backward compat in CI scripts
	go test -short ./...

test-integration:      ## Run the full suite including DB integration (uses testcontainers if PGMI_TEST_CONN not set)
	go test ./...

test-connection:       ## Run connection/security scenario tests (requires Docker)
	go test -tags conntest -timeout 5m ./internal/db/conntest/...

test-azure:            ## Run Azure Entra ID tests (requires Azure credentials)
	go test -tags azure -timeout 10m ./internal/db/conntest/...

test-all: test test-connection  ## Run unit + connection tests

lint:                  ## Run linter (cross-platform: catches issues that only manifest on Linux)
	golangci-lint run
	GOOS=linux golangci-lint run

build:                 ## Build pgmi binary
	go build -o pgmi ./cmd/pgmi

release-ready:         ## Full pre-release gate: lint, test, connection tests, build
	$(MAKE) lint
	$(MAKE) test
	$(MAKE) test-connection
	$(MAKE) build

doctor:                ## Smoke test development environment
	@echo "=== pgmi Development Environment ==="
	@echo ""
	@printf "Go:           "; go version 2>/dev/null || echo "NOT INSTALLED"
	@printf "Docker:       "; docker info --format '{{.ServerVersion}}' 2>/dev/null || echo "NOT AVAILABLE (tests will auto-skip)"
	@printf "golangci-lint: "; golangci-lint --version 2>/dev/null || echo "NOT INSTALLED (lint will fail)"
	@printf "PGMI_TEST_CONN: "; if [ -n "$$PGMI_TEST_CONN" ]; then echo "$$PGMI_TEST_CONN"; else echo "NOT SET (will use testcontainers)"; fi
	@echo ""
	@echo "go vet:"; go vet ./... && echo "  OK" || echo "  ISSUES FOUND"

build-clean:           ## Clean Go cache and rebuild (use after template changes)
	go clean -cache
	go build -o pgmi ./cmd/pgmi

sync-ai:               ## Refresh local .claude/skills/ from the tracked embedded skills
	@echo "Refreshing local AI skills from tracked source..."
	@bash scripts/sync-ai-content.sh

diagrams:              ## Re-export docs/diagrams/*.drawio to .drawio.svg (requires draw.io Desktop)
	@bash scripts/export-diagrams.sh
