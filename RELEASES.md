# Releases

## v0.10.1 — 2026-06-14

28 commits, 63 files changed, +1,206 / -493 lines since v0.10.0.

A patch release: a correctness fix to the advanced template's try-cast operator, a hardened release pipeline, and a documentation/contract accuracy pass. The session API contract is **unchanged at v1**; existing `deploy.sql` files continue to work without modification.

### Highlights

- **Try-cast operator renamed `?|` → `api.?>`** (advanced template, `lib/common/cast.sql`). The old `?|` name collided with PostgreSQL's built-in jsonb `?|` operator, so any handler that loaded both the jsonb operators and the try-cast operators had ambiguous resolution. The operator is now `api.?>`, created in the `api` schema so it resolves inside handler bodies (handler `search_path`: `api, internal, extensions, pg_temp`). Covers `uuid`, `boolean`, `integer`, `bigint`, `numeric`, `interval`, `timestamp`, `timestamptz`.
- **Release pipeline now gated.** Tagging `v*` previously went straight to GoReleaser with no checks. A `verify` job now runs lint, the full test suite, the connection/security suite, and `goreleaser check` *before* the publish job — a broken config or red test fails the gate and skips publishing instead of cutting a bad release.
- **`pgmi ai contract` accuracy.** The machine-readable contract now emits `pgmi_is_sql_file()` and `pgmi_persist_test_plan()`, matching the documented v1 API surface; AI assistants no longer see a contract that's narrower than the docs.

### Fixes

- **`install.sh` now verifies the download checksum**, matching `install.ps1` (the POSIX installer previously skipped integrity verification).
- **`--force` help** corrected to describe the actual 5-second countdown.
- **Exit-code documentation synced** across `docs/CLI.md`, `AGENTS.md`, and `docs/QUICKSTART.md` — codes `15`/`16`/`130` and the real `pgmi <ver> (compat <n>)` version format.
- Numerous documentation corrections: GitHub anchor links, template positioning, `pgmi ai setup` assistant/flag coverage, and `COMMENT ON` across basic and advanced template objects.

### Internal

- Go bumped to 1.25 in all workflows; noop GoReleaser hook removed.
- GoReleaser config modernized to current v2 schema (`archives.formats`, `homebrew_casks`) so `goreleaser check` passes clean in the release gate.
- `.gomodcache/` (local `GOMODCACHE`) added to `.gitignore`.
- `interface{}` → `any`, manual contains loops → `slices.Contains`.
- New contract column-level and default-value drift tests.

### Distribution change

- **Homebrew is now a Cask, macOS-only.** GoReleaser removed the Homebrew *formula* path (`brews`), so pgmi ships as a Cask: `brew install --cask vvka-141/pgmi/pgmi`. **Linux Homebrew is no longer supported** — Linux users should use the APT repo (`dl.cloudsmith.io/.../pgmi`), the install script, a GitHub Releases archive, or `go install`.

### Verification gates

| Gate | Result |
|------|--------|
| `go test -short ./...` (27 packages) | PASS |
| `go build ./cmd/pgmi` | PASS |
| `go test ./...` (full integration) / `goreleaser check` | **gated in CI** (new `verify` job runs before publish) |

### Upgrade notes

- No `deploy.sql` changes required — session API is at v1.
- **Advanced template only:** if you used the try-cast operator by its `?|` name in handler SQL, switch to `api.?>`. The `common.try_cast(...)` function form is unchanged.

## v0.10.0 — 2026-05-04

20 commits, 100 files changed, +4,707 / -1,798 lines since v0.9.1.

This release is the result of a 360° code review and a multi-wave refactor. It pulls in significant security fixes from the Go review (concurrent-deploy hardening, AWS RDS IAM token refresh, symlink rejection on file scanning), unblocks managed cloud Postgres (Wave G removed `plv8`), tightens the entire CLI voice for a Postgres-expert audience (three-tier rewrite of help text, error messages, approver prompts, NOTICE output), and lands a Wave 4 API-key authentication system in the advanced template.

The session API contract (`pgmi_source_view`, `pgmi_plan_view`, `pgmi_test_plan`, etc.) is **unchanged at v1**. Existing `deploy.sql` files continue to work without modification.

### Highlights

- **Cloud-compat verified**: removed `plv8` from advanced template; basic template deploys on all major managed providers. Advanced template requires superuser for the DDL event trigger — see the [cloud compatibility matrix](docs/PRODUCTION.md) for provider-specific details and the one-file workaround. Integration test now exercises the advanced template against a stock Postgres testcontainer end-to-end (19 SQL test steps, idempotent redeploy).
- **API-key authentication** for machine-to-machine access in the advanced template: SHA-256 hashed keys with constant-time compare, tied to the existing `auth.user_identity` provider hierarchy, with disable / re-enable / revoke / expiry / activation-window lifecycle.
- **Entity standards via DDL event triggers**: `core.entity_id` UUID domain marker triggers a superuser-escalated event handler that uniformly stamps `created_at`, `updated_at`, `deleted_at`, ownership columns, and audit triggers across every table that uses the marker. Replaces the old inheritance-based approach.
- **REST/RPC/MCP routing**: handler registry (`api.handler`) backs all three protocols, with JSON-RPC 2.0 envelope semantics, MCP `tools/call` spec compliance (`result.isError`, `structuredContent`, `_meta.tags`, RFC 6570 URI templates), and the same tag-based dispatch wiring across protocols.

### Security & correctness fixes (Wave H)

- **`pg_try_advisory_lock` for concurrent deploys**: two simultaneous `pgmi deploy` runs against the same database now fail fast with `ErrConcurrentDeploy` (exit code 15) instead of corrupting each other's session state.
- **AWS RDS IAM token refresh**: tokens previously expired after 15 minutes mid-deploy on long-running migrations because pgxpool dialed new backends with stale credentials. New `BeforeConnect` hook re-mints tokens on every backend dial; `MaxConnLifetime` capped to remaining token validity.
- **Symlink rejection on file scanning**: `*.sql` symlinks pointing at `/etc/shadow` (or any path outside the project) are now rejected at the loader. `os.Lstat` + `io.LimitReader` + post-read size verification.
- **Macro detection respects string literals and comments**: the preprocessor used to expand `CALL pgmi_test()` even when the text appeared inside an `EXECUTE` quoted string or a `RAISE NOTICE`. New `RedactForMacros` produces a length-preserving mask so byte positions stay correct; literals and comments are blanked out before the regex scan.
- **Race-free signal handling**: `--force` and signal interrupts now use `sync/atomic.Bool` instead of plain bool. `go test -race` clean.
- **Password redaction in `FormatError`**: libpq `password=...` and URI `user:pass@host` fragments are scrubbed before any error message reaches the user.

### Cloud compatibility (Wave G)

- `plv8` extension dependency removed from the advanced template. Required extensions are now `uuid-ossp`, `pgcrypto`, `pg_trgm`, `hstore` — all available on managed clouds.
- Cloud compatibility matrix added to `docs/PRODUCTION.md` covering RDS / Aurora, Azure Flexible Server / Cosmos, Google Cloud SQL / AlloyDB, Supabase, Neon.
- Integration test gate that previously skipped the advanced template when `plv8` was unavailable has been removed; the advanced template is now actually exercised in CI.

### CLI voice overhaul (Tier 1, 2, 3)

A three-tier rewrite of every user-facing string against an explicit voice guide for Postgres experts, CLI experts, and migration engineers. **No breaking changes to flags, exit codes, or behavior** — only message text.

**Tier 1 (high-visibility surfaces):**
- Root `pgmi --help`: dropped marketing prose, philosophy paragraph, and exit-code wall. New help is 13 lines: one mechanical description, three example commands, libpq env-var line. Empty `pgmi` (no args) shows the ASCII logo + brief on TTY only; pipes/CI/`PGMI_NO_BANNER=1` get the clean help fallback.
- `pgmi deploy --help`: tightened from 40+ lines to 23, exit codes documented here (where troubleshooters look), parameter precedence rule one-liner.
- `pgmi init --help`: 12 lines, examples-first, fixed output stream discipline (status to stderr, file tree to stdout).
- Connection error messages tightened from 7-9 lines each to 2-3 lines while preserving `errors.Is` chain via a custom `connError` type.

**Tier 2 (subcommands, approvers, init wizard):**
- `templates`, `metadata`, `ai`, `version`, `config`, `completion` subcommand help all rewritten.
- Template descriptions corrected — `basic` and `advanced` now show their actual current structure, not the year-old stale layout.
- Interactive approver: `WARNING: about to DROP and RECREATE database "mydb". This deletes all data. Type the database name to confirm:` — Postgres-style identifier quoting, no emoji, no redundancy.
- `--force` ASCII danger banner: kept as a safety-critical interruption surface, but now TTY-gated (same gate as the empty-command logo). Plain countdown in CI/pipes.

**Tier 3 (logger, NOTICE prose, connection wizard):**
- Three "successfully" violations removed from deployer logger output.
- Single-quoted DB names → Postgres-style `%q` throughout deployer/session.
- DROP/CREATE DATABASE log lines now print the actual SQL statement so operators can correlate with PostgreSQL state.
- Connection wizard: `✓ Connected successfully` → `✓ Connected`. The cloud-auth pseudo-success message now honestly reports `X authentication configured (not tested — token is fetched at deploy time)` instead of misleading "ready" prose.

### CLI infrastructure

- **New exit codes**: `15` (ExitConcurrentDeploy), `130` (ExitInterrupted, SIGINT/Ctrl-C convention).
- **`pgmi metadata`**: `scaffold`, `validate`, `plan` subcommands operate purely on the filesystem (no DB connection) for inspecting `<pgmi-meta>` blocks, validating XML/uniqueness, and previewing execution order.
- **`pgmi ai`**: machine-readable documentation surface for AI coding assistants (overview, skills, templates) — designed for Claude Code, Copilot, Gemini, Cursor to ingest as context.
- **`-d/--database` reserved for target**: connection-string database is the *maintenance* database, `-d` is the *target*. Two-database pattern documented and tested.
- **`pgmi version` machine-greppable**: `pgmi version | head -1` returns just `pgmi 0.10.0 (compat 1)`.

### Behavioral changes worth flagging

- **`pgmi init` output streams**: status messages (`Wrote ./demo (basic template).`, `Next:`) now go to **stderr**; the file tree stays on stdout. Scripts that captured init output verbatim may need adjustment (no users yet, but flagged).
- **Connection error messages no longer print "Original error: %w" trailer**. Visible message is the curated 2-liner; the original error is still in the chain via `errors.Unwrap()`.
- **Connection wizard for non-standard auth (Azure/AWS/Google) no longer claims a successful connection**. It now correctly reports that the configuration was saved and the token will be fetched at deploy time.
- **`pgmi templates list` and `templates describe` now emit usage hints to stderr**, structured data to stdout. `pgmi templates list | awk` works as intended.

### Documentation

- New `docs/API-KEYS.md` documenting the Wave 4 authentication system.
- Cloud compatibility matrix in `docs/PRODUCTION.md`.
- 10 stale documentation references corrected across `docs/`.
- AI-digestible content in `internal/ai/content/` synced from `.claude/skills/`.
- New embedded skill `pgmi-cli-voice` documenting the voice rules going forward.

### Test improvements

- Advanced template integration test: previously gated on `plv8` (silently skipped on stock Postgres), now runs unconditionally and verifies a clean deploy + idempotent redeploy + 19 SQL test steps.
- 4 new test cases for `ExitInterrupted` (130) and `ExitConcurrentDeploy` (15) sentinel mappings.
- Connection-scenario suite (`internal/db/conntest`): 14 tests covering mTLS, sslmode precedence, SSL modes, standard auth — all green.

### Internal

- ASCII logo asset retained, exposed only on the empty `pgmi` command in interactive TTY contexts. `PGMI_NO_BANNER=1` provides explicit operator opt-out.
- `--force` skull asset retained, gated identically. The skull is a safety-critical interruption surface, not decoration; visual jolt is the feature.

### Verification gates

All of the following pass on this branch:

| Gate | Result |
|------|--------|
| `go test -short ./...` (27 packages) | PASS |
| `go test ./...` (full integration, testcontainer) | PASS |
| `go test -tags conntest ./internal/db/conntest/...` (14 scenarios) | PASS |
| `go test ./internal/scaffold -run TestTemplateDeployment` (basic + advanced) | PASS |
| `golangci-lint run ./...` (Windows) | clean |
| `GOOS=linux golangci-lint run ./...` | clean |
| Cross-platform builds (Windows amd64, Linux amd64+arm64, macOS amd64+arm64) | clean |
| `go test -race ./...` | **deferred to CI** (Windows host has no gcc; gate must run on Linux) |

### Upgrade notes

- No `deploy.sql` changes required — session API is at v1.
- If you script `pgmi init` output: status text moved to stderr.
- If you have automation that grepped specific error message text, two rewordings to be aware of: connection errors are shorter; connection-wizard cloud-auth status is now honest about not having tested.

---

## Earlier releases

See `git log --tags --oneline` for the v0.7 / v0.8 / v0.9 history. This file starts the explicit per-release changelog at v0.10.0.
