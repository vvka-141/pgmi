# 0001: Crate layout

Status: proposed, 2026-11-10. Ticket: PGMI-829.
Checked on crates.io / static.rust-lang.org, 2026-11-10: Rust stable 1.99.1
(2026-10-15); aws-config 1.13.1 declares rust-version 1.94.1, the highest of
the crates chosen in these records (google-cloud-auth 1.18.0: 1.91,
serde-saphyr 2.1.1: 1.89, ratatui 0.30.2: 1.88). The crates.io name `pgmi` is
held by the owner (placeholder 0.0.1); `pgmi-shared` is free.

## Context

The Go code (`go.mod`, `cmd/`, `internal/`, `pkg/`) stays at the repository
root until switch day, then goes. `shared/` sits at the root and both binaries
embed it unchanged. A crate published to crates.io may only contain files
under its own directory, so whatever packages `shared/` must have its
`Cargo.toml` inside `shared/`. The advanced template's Rust gateway ships as
`Cargo.toml.scaffold`, so cargo does not see it as a package.

## Options

1. **Workspace rooted in `rust/`.** Keeps Rust out of the Go tree, but
   `shared/` lies outside it: the shared crate would need
   `package.workspace = "../rust"`, cargo commands only work from `rust/`, and
   switch day moves everything again.
2. **Virtual workspace at the repository root.** One `Cargo.toml` next to
   `go.mod`. Cargo and Go ignore each other's files. Switch day only deletes
   the Go folders.
3. **Several library crates** (connection, project, deploy, serve) instead of
   one binary crate. Faster parallel builds in theory, but the cloud SDKs
   dominate build time, and pgmi ships no Rust library API at the switch
   (`--json` is the integration surface; the Go library is not carried over).

## Decision

Option 2, with one binary crate and two support crates:

```
Cargo.toml                    virtual workspace, resolver 3
Cargo.lock                    committed
rust-toolchain.toml           1.99.1 for CI and releases
rust/pgmi/                    package `pgmi`: the binary, all pgmi code as modules
shared/Cargo.toml, build.rs, src/lib.rs   package `pgmi-shared` (record 0010)
```

- `workspace.exclude = ["shared/templates/advanced/gateways/rust"]`, so a
  developer who renames the scaffold manifest locally does not break the
  workspace.
- Edition 2024 everywhere. `rust-version = "1.94.1"`, the highest requirement
  among the dependencies. Resolver 3 (the 2024 default) picks dependency
  versions that respect it, which helps `cargo install pgmi` on older
  toolchains. Raise it only when a deliberate dependency update needs it.
- Modules in `rust/pgmi/src/`: `cli`, `env`, `config` (pgmi.yaml, params),
  `conn` (resolution, pgpass, tls, cloud/{azure,aws,google}), `project`
  (walk, ignore, lexer, checksum, metadata), `deploy` (session, execute,
  overwrite, lock), `output`, `serve`, `tui`, `error`.
- Release profile: `lto = "thin"`, `codegen-units = 1`, `strip = true`,
  `panic = "unwind"` (record 0002 depends on unwinding).

## Consequences

- The root `.gitignore` needs `/target/`; it has none today.
- New files land in `shared/` (`Cargo.toml`, `build.rs`, `src/`). Rust's
  embed list excludes them (record 0010). Whether Go's embed patterns would
  pick them up is an open question: it must be checked in Go before the
  files are added, without changing Go's embedded set.
- Splitting a module out into its own crate later is cheap. Start with one
  crate.

## Serves

Exit code table; templates, version, ai, completion, help; Template
deployments (the binary that embeds `shared/`).
