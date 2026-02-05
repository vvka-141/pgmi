.PHONY: test test-short test-integration test-connection test-azure test-all lint build release-ready doctor build-clean

test:                  ## Run unit tests (no database required)
	go test ./...

test-short:            ## Run unit tests, skip slow tests
	go test -short ./...

test-integration:      ## Run all tests including DB integration (requires PGMI_TEST_CONN)
	go test ./...

test-connection:       ## Run connection/security scenario tests (requires Docker)
	go test -tags conntest -timeout 5m ./internal/db/conntest/...

test-azure:            ## Run Azure Entra ID tests (requires Azure credentials)
	go test -tags azure -timeout 10m ./internal/db/conntest/...

test-all: test test-connection  ## Run unit + connection tests

lint:                  ## Run linter
	golangci-lint run

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
	@printf "Docker:       "; docker info --format '{{.ServerVersion}}' 2>/dev/null || echo "NOT AVAILABLE (connection tests will fail)"
	@printf "golangci-lint: "; golangci-lint --version 2>/dev/null || echo "NOT INSTALLED (lint will fail)"
	@printf "PGMI_TEST_CONN: "; if [ -n "$$PGMI_TEST_CONN" ]; then echo "$$PGMI_TEST_CONN"; else echo "NOT SET (integration tests need this)"; fi
	@echo ""
	@echo "go vet:"; go vet ./... && echo "  OK" || echo "  ISSUES FOUND"

build-clean:           ## Clean Go cache and rebuild (use after template changes)
	go clean -cache
	go build -o pgmi ./cmd/pgmi
