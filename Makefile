.PHONY: test test-short test-integration test-connection test-azure test-all lint build release-ready vulncheck doctor build-clean sync-ai diagrams

# The version CI lints with. Kept identical in .github/workflows/{ci,release}.yml —
# TestLintVersionPinsAgree fails if the three drift apart. Lint findings are
# version-dependent, and golangci-lint v2 rejects this v1 config outright.
GOLANGCI_VERSION := v1.64.8

test:                  ## Run unit tests only (no database, short mode)
	go test -short -tags pgmi_testhooks ./...

test-short:            ## Alias for `test` — kept for backward compat in CI scripts
	go test -short -tags pgmi_testhooks ./...

test-integration:      ## Run the full suite including DB integration (uses testcontainers if PGMI_TEST_CONN not set; conntest/azure tags need their own targets)
	PGMI_REQUIRE_DB=1 go test -tags pgmi_testhooks ./...

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

release-ready:         ## Full pre-release gate: lint, full suite, connection tests, release notes, build. Requires TAG=vX.Y.Z.
	@if [ -z "$$TAG" ]; then \
		echo "release-ready: TAG is required (e.g. make release-ready TAG=v0.12.0)" >&2; \
		exit 1; \
	fi
	$(MAKE) lint
	$(MAKE) test-integration
	$(MAKE) test-connection
	./scripts/release-notes.sh "$$TAG" > /dev/null && echo "release notes: OK for $$TAG"
	$(MAKE) vulncheck
	@if command -v goreleaser > /dev/null 2>&1; then \
		goreleaser check; \
	else \
		echo "goreleaser check: SKIPPED (not installed; CI runs it on the tag)"; \
	fi
	$(MAKE) build
	@echo ""
	@echo "Not covered here — the tag workflow runs these:"
	@echo "  * the three end-to-end example gates (.github/workflows/examples.yml)"
	@echo "  * the full snapshot build — archives, .deb, checksums for all 6 targets"
	@echo "    (.github/workflows/snapshot.yml; goreleaser check here only reads the config)"
	@echo "  * the race detector (needs CGO)"
	@echo "  * the tag-is-on-main provenance check"

vulncheck:             ## Scan dependencies for known vulnerabilities (blocks on reachable findings only)
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

doctor:                ## Smoke test development environment
	@echo "=== pgmi Development Environment ==="
	@echo ""
	@printf "Go:           "; go version 2>/dev/null || echo "NOT INSTALLED"
	@printf "Docker:       "; docker info --format '{{.ServerVersion}}' 2>/dev/null || echo "NOT AVAILABLE (tests will auto-skip)"
	@printf "golangci-lint: "; \
		if ! golangci-lint --version 2>/dev/null; then \
			echo "NOT INSTALLED (lint will fail)"; \
		elif ! golangci-lint --version 2>/dev/null | grep -q "version $(GOLANGCI_VERSION) "; then \
			echo "  ^ CI lints with $(GOLANGCI_VERSION); findings will differ from CI"; \
		fi
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
