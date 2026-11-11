# 0022: Library layout, seams and the internal refactor

Status: accepted, 2026-11-11 (owner delegated the refactor on PGMI-857).
Ticket: PGMI-857. Audit: `.private/reviews/rust-857-audit-2026-11-11.md`
(private overlay).
Builds on 0001 (crate layout), 0002 (errors), 0003 (runtime), 0017 (MCP),
0019 (output). Supersedes the module list in 0001.

## Context

The owner decided on 2026-11-11 that parity with Go means engineering
parity: the Rust code must be at least as robust and as testable inside as
Go, and idiomatic Rust. The audit for PGMI-857 found the leaves (lexer,
ignore matcher, checksum, wire, tap) idiomatic and well tested, and the
orchestration layer hard to test: no library target, no filesystem seam,
the MCP server bound to stdin/stdout, approval and output bound to the
terminal, clap structs used as the deploy request, and the `--json` record
built by hand. 108 Rust unit tests stand against 1,148 Go test functions;
the Go tests that would catch two exit-code bugs (unreadable files,
parameter keys) have no counterpart because their subject cannot be
reached without a disk or a server.

## Options

1. **Keep one binary crate, add tests through the binary.** No churn, but
   tests stay slow and coarse, fuzz targets keep `#[path]` includes, and the
   seams the Go tests rely on never appear.
2. **Split into library crates** (`pgmi-conn`, `pgmi-project`,
   `pgmi-deploy`, `pgmi-serve`). Enforced boundaries, but each crate needs a
   public API and versioning, the cloud SDKs still dominate build time, and
   nothing outside pgmi uses these parts (0001).
3. **One package with a lib target and a thin bin, plus a handful of
   traits where a test double exists.** Integration tests and fuzz targets
   link the library; modules get `pub(crate)` surfaces; seams appear only
   where a unit test would otherwise need the world.

## Decision

Option 3.

### Layout

    rust/pgmi/
      Cargo.toml            [lib] name = "pgmi"; [[bin]] name = "pgmi"
      src/main.rs           sigpipe, panic hook, catch_unwind, Env snapshot,
                            the .env export closure (the only unsafe), exit code
      src/lib.rs            pub fn run(args, env, export) -> u8; module tree;
                            #![forbid(unsafe_code)]; VERSION
      src/error.rs          Error, Kind, chain(), redact(), Secret
      src/env.rs            Env
      src/vfs.rs            Vfs, OsFs, EmbeddedFs; MemFs (cfg(test) or feature testkit)
      src/output.rs         Reporter<sink>, NoticeLog, Notice, Severity, DeployRecord
      src/templates.rs      Template enum, catalogue, render() (pure)
      src/duration.rs       Go-style durations (cli, yaml, MCP)
      src/project/          walk, ignore, lexer, checksum, metadata, script
      src/config/           yaml, params, dotenv, edit
      src/conn/             resolve, parse, pgpass, tls, tap, dial, retry, cloud/
      src/deploy/           Request, run(), session, execute, overwrite, Approver
      src/serve/            server (transport-generic), wire, tools (Toolbox)
      src/cli/              clap types and From<DeployArgs> for deploy::Request
      src/commands/         init, info, metadata, templates, ai, config, completion
      src/tui/              wizards (state machines), screen, event loops
      fuzz/                 links pgmi; no #[path]
      tests/                library-level integration tests (real PostgreSQL)

Dependencies point down only: `cli` -> `commands`/`deploy`/`serve` ->
`conn`, `project`, `config`, `templates` -> `vfs`, `env`, `error`,
`output`, `duration`. Nothing below `commands` names clap or the terminal.

`pub(crate)` is the default. `pub` only for what `tests/` and `fuzz/` link
(`serve::wire`, `conn::tap`, `conn::parse`, `serve::serve`,
`deploy::run`), each with a doc comment saying it is not a stable API.
The lib target is not published as a library API (0001 still holds).

### Abstractions and reasons

- **`Vfs` (read side of a tree).** Shape as built in `vfs.rs`: `stat`,
  `read`, `read_dir` on `/`-separated paths relative to the root, plus
  `display`. Implementations: `OsFs` (rooted at the project, `OsFs::project`
  refuses a missing or non-directory root with exit 10), `EmbeddedFs` over
  `pgmi_shared` (a template or `skills/` as a tree), `MemFs` (a
  `BTreeMap<String, Node>` with files, directories, links, and
  `Node::Unreadable(io::ErrorKind)` for error injection). Entries carry the
  name as `enum Name { Utf8(String), Lossy(String) }`. `OsFs::read_dir`
  returns per-entry errors instead of skipping them; the walker turns an
  unreadable entry into exit 10 with Go's message. Generic `&impl Vfs`
  (three impls, calls are I/O-bound; generics keep `spawn_blocking`
  closures simple: build `OsFs` inside the closure).
  Taken by: `project::walk::{scan, load}` (and the `.pgmiignore` read),
  `config::yaml::ProjectConfig::load`, `config::project_dotenv`,
  `commands::init::{check_empty, draw}`, `commands::info`,
  `commands::metadata` (read side). Not taken by: `conn::pgpass` and
  `conn::tls` (files outside the project, tested with temp dirs as now),
  `--params-file` (arbitrary paths; `params::collect` gets a
  `read: impl Fn(&Path) -> io::Result<String>` like it gets stdin), and the
  write side.
- **Writes stay `std::fs`, behind pure planners.** `templates::render
  (template, name, version) -> Vec<(String, Vec<u8>)>` is pure (as in the
  WIP `init::render`); `commands::init` writes the plan. `metadata scaffold
  --write` keeps its atomic, link-refusing writer. A write trait would have
  one implementation and no double.
- **`deploy::Request`.** Owned domain input: project path, connection
  flags, cloud request, params sources, timeout, overwrite/force,
  compat, json. `From<cli::DeployArgs>`; the MCP tool builds it directly.
  `cloud::select` and `overwrite` take it. Removes the clap re-parse in
  serve and likely makes the deploy future `Send` (retry `tokio::spawn`
  in serve; if it holds, record 0017's blocking-pool workaround goes).
- **`Approver`.** `async fn approve(&self, db: &str, token) -> Result<()>`
  (or an enum with a boxed terminal variant): `Terminal` (current prompt and
  `--force` countdown), `ConfirmedName(Option<String>)` (serve), and test
  doubles. Two real impls plus doubles justify the trait.
- **Reporter sink.** `Reporter` owns `Mutex<Box<dyn Write + Send>>`;
  `stderr()` in production, `Vec<u8>` in tests. Summary line, verbose
  prefixes and redaction become unit-testable.
- **MCP server over any transport.** `serve::serve(input: impl AsyncRead,
  output: impl AsyncWrite, tools: &impl Toolbox, stop: CancellationToken)`.
  `Toolbox::call(name, args, token) -> Outcome`; production is the existing
  `Tool` enum; tests use a slow or panicking toolbox over
  `tokio::io::duplex`. Signals cancel `stop` from a task in `run`, so no
  future is polled after completion.
- **`DeployRecord`.** One serde struct (0019), filled from typed phase
  results; the `State` bag and `record()` go. Serve reuses it.
- **Types.** `SslMode`, `ChannelBinding`, `TargetSessionAttrs`,
  `Severity`, `Template`, `IgnoreRule`, `AuthMethod` as `cloud::Provider`
  via serde; `Secret(String)` with a redacting `Debug` for `password`,
  `sslpassword`, tokens; `Spec` hosts non-empty by construction.
- **Errors.** Keep 0002's single `Error`. Add module enums only where a
  caller or test branches: `conn::parse::ParseError`,
  `conn::resolve::ResolveError`, `project::metadata::MetaError`,
  `conn::retry` classification; each has one `From<_> for Error` that picks
  the `Kind`. One `chain()` helper replaces the four flatteners.
- **Retry.** `conn::retry`: pure `transient(&Failure) -> bool` and the
  delay table; `Dialer::connect` calls it.
- **Runtime.** `lib::run` builds one multi-thread runtime after `.env` is
  exported (0003) and hands `&Runtime` to `deploy`, `serve` and the TUI
  connection test; no module builds its own.

### Refactor plan

Each step is one commit; after each: `cargo fmt --check`, `cargo clippy
-D warnings`, `cargo test --workspace`, `cargo deny check`,
`rust/check-no-c.sh`, `rust/check-package.sh`, and the black-box suite on
the Rust binary with the same passing list as before (135/141 today; no
case may move from pass to fail). Behaviour fixes are separate tickets and
land before or between steps, each with a black-box case.

0. Behaviour fixes first, done 2026-11-11: serve stop panic, unreadable
   file or directory exit 10, parameter-key validation, statements outside
   `Guard`. Unconsumed connection keys are PGMI-860.
1. **lib/bin split.** `lib.rs` with `run(args, env, export)`; `main.rs`
   keeps sigpipe, panic hook, `catch_unwind`, the unsafe export closure;
   fuzz targets link the lib; `pub` -> `pub(crate)` except the listed
   items; `VERSION` to the root; `Env::snapshot` once in `main`.
   Port `TestDeployCmdHasNoOrchestrationFlags` as a unit test on
   `Cli::command()`. Done 2026-11-11: `pub` items are `serve::wire` and
   `conn::tap` (what the fuzz targets link), `run`, `Env`,
   `install_panic_hook` and `PANIC_EXIT_CODE`; the rest is `pub(crate)`.
   clap's usage and help texts moved to `cli::usage`.
2. **Break cycles.** `cloud` under `conn`; `Notice` to `output`;
   `redact`/`chain` to `error`; `templates` data module; `duration` out of
   `cli`. No behaviour change.
3. **`Vfs`.** Landed 2026-11-11 (`vfs.rs`: `OsFs`, `EmbeddedFs`, `MemFs`
   with error injection; entry errors returned; walker, `.pgmiignore` and
   `init` read through it). Still to do: the `Name` enum, and
   `pgmi.yaml`/`.env` read through it. Port scanner group 1 (1.2) to `MemFs`; one test loads every embedded
   template through `EmbeddedFs` and checks checksums and metadata (Go
   `TestTemplateStructureWithoutFilesystem`).
4. **Templates and init.** `Template` enum; `check_empty` over `Vfs`; port
   the rest of scaffold group 9 (pure `render` and `draw` over `Vfs` landed
   with step 3).
5. **`deploy::Request`.** `From<DeployArgs>`; serve builds it directly;
   `cloud::select`, `overwrite` take it; try `tokio::spawn` for tool calls.
6. **`Approver` and Reporter sink.** Unit tests for the summary line,
   refusal texts, `--force` countdown cancel (with `tokio::time::pause`).
7. **Serve over any transport.** `serve(input, output, toolbox, stop)`;
   port MCP group 8 in-process.
8. **`DeployRecord`.** Replace `State` and `record()`; pure tests of the
   failure record (`failed_files`, `executionMode`, `phase`).
9. **Types and secrets.** Enums for connection settings and severity;
   `Secret`; non-empty hosts; port resolver group 5 and
   `FormattingNeverPrintsSecrets`.
10. **Typed module errors and `conn::retry`.** Port groups 6 and 7.
11. **One runtime.** `lib::run` owns it; `deploy::run`, `serve::run`,
    `tui::connection::run` take a handle.

Separate tickets, in parallel after steps 3 and 7: port the remaining Go
unit groups (2, 3, 4, 10, 12, wizards); a fuzz/property target for
`conn::parse`; port the content linters and the template deployment tests
of `internal/scaffold` and `internal/services` to `rust/pgmi/tests/` before
switch day.

### Left alone

- 0002's single `Error` and `Kind::exit_code()`.
- The hand-written JSON-RPC and its Go-compatible error texts (0017,
  contract); they stay inside `serve::wire`.
- `conn::tap`, `conn::tls` adapters, `conn::dial` connection logic (spike
  verified, fuzzed), the cloud providers' code.
- The pure cores: `lexer`, `ignore`, `checksum`, `script`, `metadata::parse`.
- `pgmi-shared` as a data crate; no logic beyond lookups.
- The TUI wizard state machines (`update` -> `Effect`).
- No database trait: SQL is tested against a real server.
- No further crates.

## Consequences

- Every Go unit test whose subject exists in Rust gets a Rust counterpart
  reachable without a disk, server or terminal, except database behaviour,
  which moves to library-level integration tests.
- `tests/` and `fuzz/` link the library; no file is compiled twice.
- 0001's module list, 0003's runtime note, 0017's blocking-pool note and
  0019's `DeployRecord` are brought in line with the code in the commits
  that change them.

## Serves

Engineering parity (owner decision 2026-11-11); Exit code table; serve;
Project discovery and loading; The --json record; Timeout, cancellation,
interruption.
