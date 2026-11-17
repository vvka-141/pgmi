.PHONY: build test test-db lint deny blackbox differential check doctor sync-ai diagrams

GO_REFERENCE ?= target/pgmi-go-0.14.1

build:                 ## Build the release binary (target/release/pgmi)
	cargo build --locked --release -p pgmi

test:                  ## Run the Rust tests; database tests skip without PGMI_TEST_CONN
	cargo test --locked --workspace

test-db:               ## Run the Rust tests; a missing PGMI_TEST_CONN fails instead of skipping
	PGMI_REQUIRE_DB=1 cargo test --locked --workspace
	PGMI_REQUIRE_DB=1 cargo test --locked -p pgmi --features testhooks --test panic --test google_cloud_sql --test aws_credential_process_hang

lint:                  ## Format check and clippy, also with the test hooks
	cargo fmt --all --check
	cargo clippy --locked --workspace --all-targets -- -D warnings
	cargo clippy --locked -p pgmi --features testhooks --all-targets -- -D warnings

deny:                  ## Advisories, licences, banned crates, and no C compiler on any release target
	cargo deny --locked check
	scripts/check-no-c.sh

blackbox: build        ## Run the black-box suite against target/release/pgmi (PostgreSQL from PGMI_TEST_CONN or Docker)
	cd blackbox && PGMI_BIN=$(CURDIR)/target/release/pgmi uv run --locked pytest test_cases.py \
		$$(sed -n 's|^\([^#[:space:]][^[:space:]]*\).*|--deselect=test_cases.py::test_case[cases/\1.yaml]|p' skip.txt)

$(GO_REFERENCE):
	.github/scripts/go-reference.sh $@

differential: build $(GO_REFERENCE)  ## Run pgmi and the last Go release (0.14.1) on the same inputs
	cd blackbox && PGMI_BIN=$(CURDIR)/$(GO_REFERENCE) PGMI_BIN_OTHER=$(CURDIR)/target/release/pgmi uv run --locked pytest test_differential.py

check: lint deny test-db blackbox  ## The local release gate: everything CI's Rust jobs run on Linux

doctor:                ## Show the development tools and the test database
	@printf "cargo:          "; cargo --version 2>/dev/null || echo "NOT INSTALLED (see rust-toolchain.toml)"
	@printf "uv:             "; uv --version 2>/dev/null || echo "NOT INSTALLED (needed for the black-box suite)"
	@printf "cargo-deny:     "; cargo deny --version 2>/dev/null || echo "NOT INSTALLED (cargo install --locked cargo-deny)"
	@printf "Docker:         "; docker info --format '{{.ServerVersion}}' 2>/dev/null || echo "NOT AVAILABLE"
	@printf "PGMI_TEST_CONN: "; if [ -n "$$PGMI_TEST_CONN" ]; then echo "$$PGMI_TEST_CONN"; else echo "NOT SET (database tests skip)"; fi

sync-ai:               ## Refresh local .claude/skills/ from the scaffold skill trees
	@bash scripts/sync-ai-content.sh

diagrams:              ## Re-export docs/diagrams/*.drawio to .drawio.svg (requires draw.io Desktop)
	@bash scripts/export-diagrams.sh
