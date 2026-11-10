# 0005: The patched tokio-postgres: vendoring and upstream

Status: proposed, 2026-11-10. Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: tokio-postgres 0.7.18 (2026-06-12),
tokio-postgres-rustls 0.14.0 (2026-05-21, depends on upstream
tokio-postgres); the name `pgmi-tokio-postgres` is free.

## Context

pgmi needs two things tokio-postgres 0.7.18 does not have: the transaction
status byte of every ReadyForQuery (spike A's 25-line patch) and
`require_auth` (a second small patch: record the authentication request
during startup and refuse a method not listed). Both are kept by owner
decision. pgmi is also published to crates.io, and **cargo ignores
`[patch]` sections when it builds a published crate**: `cargo install pgmi`
would compile against upstream tokio-postgres and fail.

## Options

1. **`[patch.crates-io]` pointing at a vendored copy.** Simplest in the
   repository, and other crates that use tokio-postgres's types keep working.
   Breaks `cargo install pgmi`.
2. **Vendored copy renamed `pgmi-tokio-postgres`, published to crates.io,**
   used as `tokio-postgres = { package = "pgmi-tokio-postgres", ... }`.
   `cargo install` works. Any crate built on upstream tokio-postgres's traits
   (tokio-postgres-rustls) no longer fits, so pgmi writes its own TLS adapter
   (record 0006).
3. **Wait for upstream.** Transaction status has been asked for upstream for
   years; pgmi cannot wait on it.
4. **Drop the crates.io channel** until upstream merges. Gives up a channel
   the owner has already reserved.

## Decision

Option 2.

- `rust/vendor/tokio-postgres/` holds upstream 0.7.18 unchanged in one
  commit, then each patch as its own commit. The same patches live as files
  in `rust/vendor/patches/` (`0001-transaction-status.patch`,
  `0002-require-auth.patch`).
- `rust/vendor/update.sh <version>` downloads the upstream `.crate`, applies
  the patch files and rewrites the package name. CI re-runs it for the pinned
  version and fails if the result differs from the vendored tree, so the
  fork is always "upstream plus these patches".
- Package `pgmi-tokio-postgres`, version `0.7.18-pgmi.N`, licence kept
  (MIT OR Apache-2.0), README stating that it is a fork and why. It depends on
  upstream `postgres-protocol` and `postgres-types` unchanged.
- Both patches are offered upstream as PRs when the fork is created. When an
  upstream release carries both, pgmi switches back and stops publishing the
  fork.
- Update the fork only for an upstream security fix or a release pgmi needs,
  never in passing.

## Consequences

- One more crate to publish, before `pgmi` (record 0018).
- pgmi owns a small TLS adapter instead of using tokio-postgres-rustls
  (record 0006).

## Serves

Execution contract (transaction status); Concurrency (rollback before
unlock); TLS (`require_auth`); Encrypted legacy client keys and require_auth
are kept.
