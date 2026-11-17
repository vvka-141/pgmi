# Contributing to pgmi

Thank you for your interest in contributing to pgmi. This document covers the
checks and conventions needed to prepare a change for review.

## Code of Conduct

By participating in this project, you agree to abide by our
[Code of Conduct](CODE_OF_CONDUCT.md). Report conduct incidents privately through
the [private reporting channel](https://github.com/vvka-141/pgmi/security/advisories/new)
and prefix the title with `Code of Conduct`.

## Reporting Issues

### Bug Reports

Before creating a bug report, check existing issues, then use the
[bug report form](https://github.com/vvka-141/pgmi/issues/new?template=bug.yml).
Include:

- **Clear title** describing the issue
- **Steps to reproduce** the behavior
- **Expected vs. actual** behavior
- **Environment**: pgmi version (`pgmi --version`), PostgreSQL version, OS
- **Relevant logs** or error messages

### Feature Requests

Use the
[feature request form](https://github.com/vvka-141/pgmi/issues/new?template=feature.yml)
and:

- Check existing issues for similar requests
- Describe the use case and problem you're solving
- Explain how it aligns with pgmi's philosophy (execution fabric, not migration framework)

### Security Issues

For security vulnerabilities, please see [SECURITY.md](SECURITY.md). Do not open public issues for security problems.

## Development setup

pgmi is written in Rust: one binary, built from `crates/pgmi`, with the files
it embeds (templates, skills, the session SQL) in the `pgmi-shared` crate under
`crates/pgmi-shared/`. The crate layout and its main abstractions are described in
[record 0022](decisions/0022-library-layout-and-seams.md). You need:

- `rustup`; it installs the toolchain pinned in `rust-toolchain.toml`, with
  rustfmt and clippy. No C code is compiled: every dependency is pure Rust,
  and `scripts/check-no-c.sh` keeps it that way. The build needs only the
  platform linker (`cc` on Linux, the Xcode command-line tools on macOS, the
  MSVC build tools on Windows). The minimum supported Rust
  version is `rust-version` in the root `Cargo.toml`.
- `cargo-deny` 0.20.2 (`cargo install --locked cargo-deny@0.20.2`)
- A PostgreSQL 18 server for the database tests, named by `PGMI_TEST_CONN`
- `uv` and Docker for the black-box suite

The Makefile wraps the common commands:

| Target | What it runs |
|---|---|
| `make build` | `cargo build --release -p pgmi`; the binary is `target/release/pgmi` |
| `make test` | `cargo test --workspace`; database tests skip without `PGMI_TEST_CONN` |
| `make test-db` | the same with `PGMI_REQUIRE_DB=1`: a missing database fails the run |
| `make lint` | `cargo fmt --check` and clippy with `-D warnings`, also with `--features testhooks` |
| `make deny` | `cargo deny check` and `scripts/check-no-c.sh` |
| `make blackbox` | builds the binary and runs the black-box suite against it |
| `make differential` | the Rust binary and the released Go 0.14.1 binary on the same input |
| `make check` | `lint`, `deny`, `test-db` and `blackbox` |
| `make doctor` | checks the development environment |
| `make sync-ai` | refreshes the local `.claude/skills/` mirror from the scaffold skill trees |
| `make diagrams` | exports the draw.io diagrams |

The tests that deploy templates take more locks than PostgreSQL allows by
default, so start the test server with `-c max_locks_per_transaction=256`:

```bash
docker run -d --name pgmi-test -p 55961:5432 -e POSTGRES_PASSWORD=postgres \
  postgres:18 -c max_locks_per_transaction=256
export PGMI_TEST_CONN=postgresql://postgres:postgres@127.0.0.1:55961/postgres
export PGMI_REQUIRE_DB=1     # fail, rather than skip, when the database is missing
```

**Ports.** pgmi's development uses two fixed host ports, chosen so they do
not clash with other PostgreSQL work on the same machine (5432 to 5434 are
common defaults):

| Port | Used by |
|---|---|
| 55961 | the development and test server above (`PGMI_TEST_CONN`, `.env.example`) |
| 55962 | the throwaway server `scripts/verify-walkthrough.sh` starts to run the README's "See it work" blocks (`WALKTHROUGH_PORT` overrides it) |

The black-box suite's extra servers and fakes take a free port from the
system and need neither.

### The black-box suite

`blackbox/` runs the built binary from outside against PostgreSQL and checks
exit codes, output and database state. A change in what pgmi does from a
user's point of view gets a case there ([blackbox/README.md](blackbox/README.md)):

```bash
make blackbox                                    # build, then run every case
cd blackbox
PGMI_IMPL=rust PGMI_BIN="$PWD/../target/release/pgmi" uv run pytest
PGMI_IMPL=rust PGMI_BIN="$PWD/../target/release/pgmi" uv run pytest -k 13-sql   # one case
```

`PGMI_IMPL=rust` applies a case's `expect.rust` keys. CI deselects the few
cases listed in `blackbox/skip.txt`.

### The gates CI runs

The `Rust` job in `.github/workflows/ci.yml` runs, and a change passes, all of:

```bash
cargo fmt --all --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo clippy --locked -p pgmi --features testhooks --all-targets -- -D warnings
cargo test --locked --workspace
cargo test --locked -p pgmi --features testhooks --test panic --test google_cloud_sql --test aws_credential_process_hang
cargo deny --locked check          # advisories, licences, no C
scripts/check-no-c.sh                 # no C compiler on any release target
scripts/check-package.sh              # packaged shared files equal tracked files
```

Other jobs:

- the black-box suite on PostgreSQL 18 and 15;
- `cargo check` with the minimum Rust version (`rust-version` in `Cargo.toml`);
- the unit tests on macOS and Windows;
- fuzzing: each parser for two minutes with a pinned nightly
  (`cd fuzz && cargo +nightly fuzz run conn_parse`; the targets are
  in `fuzz/fuzz_targets/`);
- the differential run and the bad-weather cases (fault injection, locks,
  signals) against the last Go release, 0.14.1. They do not block a merge;
  a difference nothing explains, or a Rust result worse than Go's in bad
  weather, is a finding.

The design records behind the code are in
[decisions/](decisions/README.md). The Go implementation
ended with 0.14.1; its sources are at the `go-legacy` tag.

## Code Style

### Errors

Every failure is a `crate::error::Error` with a `Kind`, and the kind alone
decides the exit code (`crates/pgmi/src/error.rs`; record
[0002](decisions/0002-errors-and-exit-codes.md)). A message gives
context first and names the next step; secrets never reach it.

### Testing

- Unit tests sit next to the code; tests that need a database or the built
  binary are in `crates/pgmi/tests/` and use the helpers in `tests/common/`.
- Test names say the behaviour: `a_token_without_an_expiry_is_fetched_for_every_connection`.
- A regression test fails without the fix: prove it once by reverting the fix.

### Agent skills

pgmi ships skills that coding agents copy from: the shared scaffold skills
(`crates/pgmi-shared/skills/`) and each template's own (`.agents/skills/` under
`crates/pgmi-shared/templates/<name>/`). An agent writes whatever a skill shows, so a
change that removes, renames or reshapes an API updates every skill that names
it **in the same commit**:

1. Grep both skill trees for each name the change removes or renames:
   functions, views, handler metadata keys, headers, parameters, CLI commands,
   file paths.
2. Rewrite each hit to the new shape, or drop it. Do not keep the old name as a
   "retired" note; say what to do now.
3. Add each removed name to `RETIRED_APIS` in
   `crates/pgmi/src/content_tests/snippets.rs`, with one planted line in
   `retired_api_hits_flags_each_term`. `skills_name_no_retired_api` then keeps
   it out of every shipped skill.
4. A test or SQL example that changed runs: mark it `sql runnable target=...`
   so `crates/pgmi/tests/skill_blocks.rs` deploys it, or check it against a live
   database.
5. Examples that teach template patterns use project, task and document.
6. Run `make sync-ai` to refresh the local mirror in `.claude/skills/` from the
   scaffold trees.

## Git Workflow

This project uses a simplified gitflow:

- `main` - stable production releases (tagged with `vX.Y.Z`)
- `feature/*` - feature branches (created from and merged to `main`)
- `hotfix/*` - urgent production fixes

**Workflow:**
1. Create feature branch from `main`: `git checkout -b feature/my-feature`
2. Implement changes with tests
3. Push and create PR to `main`
4. After merge, maintainers create a semantic version tag. See
   [Release Guide](RELEASES.md).

## Commit Messages

Follow conventional commits format:

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`

Examples:
- `feat(cli): add --timeout flag to deploy command`
- `fix(rust): refuse a PGPORT that is empty`
- `refactor(rust): typed causes for resolve refusals`
- `test(blackbox): a frozen server still ends the run with exit 16`

## Pull Requests

1. Create a feature branch from `main`
2. Implement your changes with tests
3. Run `make check` with `PGMI_TEST_CONN` set: the gates above with
   `PGMI_REQUIRE_DB=1`, and the black-box suite against your build
4. If the change affects scaffolding templates, embedded SQL, or deploy/test
   execution, the template tests in `cargo test` must have run against a
   database, not skipped (`cargo test --locked -p pgmi --test template_deploy`)
5. Update documentation in the same change as the behavior it describes
6. Submit a PR to `main` with a clear description and verification notes

The cargo commands work the same in PowerShell on Windows.

## Reading release download counts

The download counts on GitHub release assets include our own CI. Every
`verify-install.yml` run (after each release and weekly) installs through
Homebrew, `install.sh` (twice) and `install.ps1`, which downloads the macOS
arm64 archive once, the Linux amd64 archive twice and the Windows amd64 zip
once, plus `checksums.txt`. Subtract those runs before reading a count as
adoption.

## Questions?

- Read [Support](SUPPORT.md), then use the
  [question form](https://github.com/vvka-141/pgmi/issues/new?template=question.yml)
- Check existing issues first
- For security issues, see [SECURITY.md](SECURITY.md)

## License

By contributing to pgmi, you agree that your contributions will be licensed under:
- **MPL-2.0** for tool code
- **MIT** for template code (in `crates/pgmi-shared/templates/`)
