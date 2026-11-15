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

pgmi is one Rust binary, `rust/pgmi`, with the files it embeds (templates,
skills, the session SQL) in the `shared` crate. You need:

- `rustup`; it installs the toolchain pinned in `rust-toolchain.toml`, with
  rustfmt and clippy. No C compiler is needed: every dependency is pure Rust,
  and `rust/check-no-c.sh` keeps it that way. The minimum supported Rust
  version is `rust-version` in the root `Cargo.toml`.
- `cargo-deny` 0.20.2 (`cargo install --locked cargo-deny@0.20.2`)
- A PostgreSQL 18 server for the database tests, named by `PGMI_TEST_CONN`
- `uv` for the black-box suite

Start with:

```bash
cargo build --locked --release -p pgmi     # target/release/pgmi
cargo test --locked --workspace            # database tests skip without PGMI_TEST_CONN
```

The tests that deploy templates take more locks than PostgreSQL allows by
default, so start the test server with `-c max_locks_per_transaction=256`:

```bash
docker run -d --name pgmi-test -p 5432:5432 -e POSTGRES_PASSWORD=postgres \
  postgres:18 -c max_locks_per_transaction=256
export PGMI_TEST_CONN=postgresql://postgres:postgres@127.0.0.1:5432/postgres
export PGMI_REQUIRE_DB=1     # fail, rather than skip, when the database is missing
```

### The black-box suite

`blackbox/` runs the built binary from outside against PostgreSQL and checks
exit codes, output and database state. A change in what pgmi does from a
user's point of view gets a case there ([blackbox/README.md](blackbox/README.md)):

```bash
cd blackbox
PGMI_IMPL=rust PGMI_BIN="$PWD/../target/release/pgmi" uv run pytest
PGMI_IMPL=rust PGMI_BIN="$PWD/../target/release/pgmi" uv run pytest -k 13-sql   # one case
```

`PGMI_IMPL=rust` applies a case's `expect.rust` keys. CI deselects the few
cases listed in `rust/blackbox-skip.txt`.

### The gates CI runs

The `Rust` job in `.github/workflows/ci.yml` runs, and a change passes, all of:

```bash
cargo fmt --all --check
cargo clippy --locked --workspace --all-targets -- -D warnings
cargo clippy --locked -p pgmi --features testhooks --all-targets -- -D warnings
cargo test --locked --workspace
cargo test --locked -p pgmi --features testhooks --test panic --test google_cloud_sql
cargo deny --locked check          # advisories, licences, no C
rust/check-no-c.sh                 # no C compiler on any release target
rust/check-package.sh              # packaged shared files equal tracked files
```

Other jobs run the black-box suite on PostgreSQL 18 and 15, check the
workspace with the minimum Rust version, run the unit tests on macOS and
Windows, and fuzz the parsers for two minutes each with a pinned nightly
(`cd rust/pgmi/fuzz && cargo +nightly fuzz run conn_parse`; the targets are
in `rust/pgmi/fuzz/fuzz_targets/`). The design records behind the code are in
[rust/docs/decisions/](rust/docs/decisions/README.md).

The Go 0.14 sources are kept until switch day for reference; they will move to
the `go-legacy` tag.

## Code Style

### Errors

Every failure is a `crate::error::Error` with a `Kind`, and the kind alone
decides the exit code (`rust/pgmi/src/error.rs`; record
[0002](rust/docs/decisions/0002-errors-and-exit-codes.md)). A message gives
context first and names the next step; secrets never reach it.

### Testing

- Unit tests sit next to the code; tests that need a database or the built
  binary are in `rust/pgmi/tests/` and use the helpers in `tests/common/`.
- Test names say the behaviour: `a_token_without_an_expiry_is_fetched_for_every_connection`.
- A regression test fails without the fix: prove it once by reverting the fix.

### Agent skills

pgmi ships skills that coding agents copy from: the shared scaffold skills
(`shared/skills/`) and each template's own (`.agents/skills/` under
`shared/templates/<name>/`). An agent writes whatever a skill shows, so a
change that removes, renames or reshapes an API updates every skill that names
it **in the same commit**:

1. Grep both skill trees for each name the change removes or renames:
   functions, views, handler metadata keys, headers, parameters, CLI commands,
   file paths.
2. Rewrite each hit to the new shape, or drop it. Do not keep the old name as a
   "retired" note; say what to do now.
3. Add each removed name to `RETIRED_APIS` in
   `rust/pgmi/src/content_tests/snippets.rs`, with one planted line in
   `retired_api_hits_flags_each_term`. `skills_name_no_retired_api` then keeps
   it out of every shipped skill.
4. A test or SQL example that changed runs: mark it `sql runnable target=...`
   so `rust/pgmi/tests/skill_blocks.rs` deploys it, or check it against a live
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
- `fix(resolver): handle empty PGPORT environment variable`
- `refactor(services): extract SessionManager from DeploymentService`
- `test(resolver): add connection resolution test cases`

## Pull Requests

1. Create a feature branch from `main`
2. Implement your changes with tests
3. Run the gates above, with `PGMI_TEST_CONN` and `PGMI_REQUIRE_DB=1` set
4. Run the black-box suite against your build
5. If the change affects scaffolding templates, embedded SQL, or deploy/test
   execution, the template tests in `cargo test` must have run against a
   database, not skipped (`cargo test --locked -p pgmi --test template_deploy`)
6. Update documentation in the same change as the behavior it describes
7. Submit a PR to `main` with a clear description and verification notes

The same commands work in PowerShell on Windows.

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
- **MIT** for template code (in `shared/templates/`)
