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
      src/output.rs         Reporter<sink>, NoticeLog, Notice, Severity
      src/templates.rs      Template enum, catalogue, render() (pure)
      src/duration.rs       Go-style durations (cli, yaml, MCP)
      src/project/          walk, ignore, lexer, checksum, metadata, script
      src/config/           yaml, params, dotenv, edit
      src/conn/             resolve, parse, pgpass, tls, tap, dial, retry, cloud/
      src/deploy/           Request, run(), session, execute, overwrite, Approver,
                            Progress, DeployRecord
      src/serve/            server (transport-generic), wire, tools (Toolbox)
      src/cli/              clap types and From<DeployArgs> for deploy::Request
      src/commands/         init, info, metadata, templates, ai, config, completion
      src/tui/              wizards (state machines), screen, event loops
      fuzz/                 links pgmi; no #[path]
      tests/                end-to-end tests: the built binary against a real PostgreSQL

Dependencies point down only: `cli` -> `commands`/`deploy`/`serve` ->
`conn`, `project`, `config`, `templates` -> `vfs`, `env`, `error`,
`output`, `duration`. Nothing below `commands` names clap or the terminal.

`pub(crate)` is the default. `pub` only for what `fuzz/` links
(`serve::wire`, `conn::tap`, `conn::parse`, `serve::serve`,
`deploy::run`; the files in `tests/` run `CARGO_BIN_EXE_pgmi`, the binary
as users run it, and link nothing), each with a doc comment saying it is not a stable API.
Parsers the fuzz targets reach through `pub(crate)` items are checked by
`invariants`, which exists only in tests and with the `fuzzing` feature the
fuzz crate turns on (PGMI-864, PGMI-870).
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
  `--params-file` (arbitrary paths, read with `std::fs` and tested in a
  `testdir::TempDir`; only stdin is injected, as a `Read`), and the write
  side. (Corrected in PGMI-869: an earlier draft promised a `read` closure
  that was never needed.)
- **Writes stay `std::fs`, behind pure planners.** `Template::render(name,
  version) -> io::Result<Vec<(String, Vec<u8>)>>` is pure; `commands::init`
  writes the plan. `metadata scaffold
  --write` keeps its atomic, link-refusing writer. A write trait would have
  one implementation and no double.
- **`deploy::Request`.** Owned domain input: project path, connection
  flags, cloud request, params sources, timeout, overwrite/force,
  compat, json. `From<cli::DeployArgs>`; the MCP tool builds it directly.
  `cloud::select` and `overwrite` take it. Removes the clap re-parse in
  serve. It did not make the deploy future `Send` (step 5, record 0017),
  so the blocking-pool workaround stays.
- **`Approver`.** `before_connecting(db)` and `async fn approve_drop(db,
  reporter, token)`: `Terminal` (current prompt and `--force` countdown),
  `ConfirmedName(Option<String>)` (serve), and test doubles. Two real
  impls plus doubles justify the trait.
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
  at the `pgmi.yaml` edge (step 9); `Secret(String)` with a redacting `Debug` for `password`,
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
   `cli`. No behaviour change. Done 2026-11-11: the dial and AWS error
   flatteners are now `error::chain`; `Severity` waits for step 9. One
   upward edge is left: `conn::cloud::select` takes `cli::DeployArgs`
   until step 5.
3. **`Vfs`.** Landed 2026-11-11 (`vfs.rs`: `OsFs`, `EmbeddedFs`, `MemFs`
   with error injection; entry errors returned; walker, `.pgmiignore` and
   `init` read through it). Still to do: the `Name` enum, and
   `pgmi.yaml`/`.env` read through it. Port scanner group 1 (1.2) to `MemFs`; one test loads every embedded
   template through `EmbeddedFs` and checks checksums and metadata (Go
   `TestTemplateStructureWithoutFilesystem`). Done 2026-11-11, with
   `Vfs::read_to_string` (fails as `std::fs::read_to_string` does) and
   `info`'s scaffold line read through `Vfs` too. Not ported:
   `TestIgnoreMatcherAgreesWithGit` (needs git and a disk),
   `TestParseIgnoreRefusesInvalidPatterns` and
   `TestParseIgnoreMatchesNonASCII`: the Rust matcher follows git's
   byte-wise wildmatch (record 0014), so `[z-a]` matches nothing instead
   of being refused, and `?` matches one byte, not one character. That
   difference from Go needs an owner decision, not a refactor.
4. **Templates and init.** `Template` enum; `check_empty` over `Vfs`; port
   the rest of scaffold group 9 (pure `render` and `draw` over `Vfs` landed
   with step 3). Done 2026-11-11: `Template::render(name, version)`
   returns the files; `init` moved to `commands::init`, where `writable`
   drops the kept files a target already has and `tree` draws the result,
   both over `Vfs`. `check_empty` lists blocking entries sorted by name,
   as Go's `os.ReadDir` does (the order was the OS's before).
5. **`deploy::Request`.** `From<DeployArgs>`; serve builds it directly;
   `cloud::select`, `overwrite` take it; try `tokio::spawn` for tool calls.
   Done 2026-11-11: `deploy::Request` holds the project path, the
   connection `Flags`, the maintenance database (serve only), the cloud
   providers asked for and the cloud `Request`, params, params files,
   timeout, overwrite, force, compat and json; `cli` converts
   `DeployArgs`, serve fills it from the tool arguments without clap.
   `cloud::select` takes the providers and the cloud request rather than
   the whole `deploy::Request`, so `conn` does not depend on `deploy`.
   Nothing below `cli`/`commands` names clap. `tokio::spawn` still failed
   rustc's auto-trait check; PGMI-869 found the two causes and `serve`
   spawns its calls (record 0017).
6. **`Approver` and Reporter sink.** Unit tests for the summary line,
   refusal texts, `--force` countdown cancel (with `tokio::time::pause`).
   Done 2026-11-11: `deploy::approve` holds the `Approver` trait
   (`Approver: Send + Sync` since PGMI-869, so `Context` keeps a
   `Box<dyn Approver>` and the deploy future is `Send`) with
   two calls: `before_connecting` (serve's `ConfirmedName` refuses a
   missing or wrong name there) and `approve_drop` right before an
   existing database is dropped (`Terminal`: the typed confirmation, or
   `--force` with the five-second countdown when a person is watching).
   `Terminal` reads its answer through an injected function, the doubles
   in the tests. `Reporter::to(sink, verbose, fancy, color)` takes any
   `Write + Send` and writes each line in one call; `Reporter::new` is
   stderr. The summary line is the pure `deploy::summary`. tokio's
   `test-util` feature is on for tests only.
7. **Serve over any transport.** `serve(input, output, toolbox, stop)`;
   port MCP group 8 in-process.
   Done 2026-11-11: `serve::serve(input: impl AsyncRead, output: impl
   AsyncWrite, tools: Arc<impl Toolbox>, stop: CancellationToken)`. The
   toolbox is an `Arc`, not a borrow, because each call runs in its own
   task. `Toolbox` has `list` (the `tools/list` array), `tool(name)` and
   `call(tool, arguments, token)`; production is `serve::tools::Tools`
   over the `Tool` enum. The reader and the writer are two futures joined
   in `serve`, so neither needs `'static`. `run` owns the signal task,
   which cancels `stop`; a stop already cancelled wins over pending
   input (`select!` is biased towards it). `serve/transport_tests.rs`
   ports MCP group 8 over `tokio::io::duplex` and byte slices with a fake
   toolbox (slow, echo, panicking), including stopping the server while a
   call runs.
8. **`DeployRecord`.** Replace `State` and `record()`; pure tests of the
   failure record (`failed_files`, `executionMode`, `phase`).
   Done 2026-11-11: `deploy::Progress` holds typed phase results
   (`Loaded`, `Provision`, `ServerInfo`, `Execution` with an optional
   `Failure`); `execute::run` fills only the `Execution`. The record is
   `#[derive(Serialize)] DeployRecord` in `deploy/record.rs` (not
   `output.rs`: it is built from `Progress`), fields in byte order of
   their names so the JSON is unchanged; `serve` sets its `message` and
   reads its counts. The `--json` output of the step 7 and step 8
   binaries was compared byte for byte (timestamps and duration
   normalised) on a success, a psql-mode failure with two candidate
   files, a test failure, an open transaction and a refused `--compat`.
   Unit tests cover the null and absent rules, the failure keys,
   `executed_text`, `failed_files` and `is_test_failure`.
9. **Types and secrets.** Enums for connection settings and severity;
   `Secret`; non-empty hosts; port resolver group 5 and
   `FormattingNeverPrintsSecrets`.
   Done 2026-11-11: `SslMode`, `ChannelBinding` and `TargetSessionAttrs`
   (`FromStr`, `Display`) live in `conn::resolve`; `Spec` keeps the mode
   asked for as `requested_sslmode: Option<SslMode>` and `Spec::sslmode()`
   derives the one used, so `cloud::check` fills the `None` instead of
   flipping a flag. `output::Severity` keeps any word it does not know as
   `Other`. `error::Secret` (redacting `Debug`, no `Display`) holds
   `password`, `sslpassword`, the Azure token, the wizard's password and
   `--connection` in `Flags`/`DeployArgs`. `Spec::hosts` is `Hosts`, a
   first host plus the rest, with no `localhost:5432` fallback.
   `conn/resolve_tests.rs` ports resolver group 5 and
   `FormattingNeverPrintsSecrets`; `EnvOverrides` is `conn::overrides`
   with its own tests (PGMI-875);
   `conn::parse` tests port group 6. PGMI-866 then ported the cases that
   had found Rust worse than Go: ADO.NET quoted values (`Password='p;a=ss'`,
   `"a""b;c"`), `Host=h:6000;Port=5999` keeping the host's port, a
   keyword/value string with spaces around `=` or a `;` inside a quoted
   value, and the token-auth refusal now naming the sslmode's source
   (`--sslmode`, `PGSSLMODE`, the string's source, `pgmi.yaml`). Still
   not ported: `PGPORT=" 5432 "` is accepted (libpq does too, Go refuses;
   a row in the contract's 0.20 table); two granular flags beside an
   environment string name only the first.
   PGMI-869: `pgmi.yaml`'s `auth_method` is an `Option<cloud::Provider>`,
   read from the text right after serde in `ProjectConfig::parse` (a serde
   error would carry serde-saphyr's location prefix and change the
   refusal's text; `databricks` has no yaml form), and `--sslmode`
   is `Option<SslMode>` through `clap::ValueEnum` (same possible-values
   error). Left as text on purpose: `sslmode` in `pgmi.yaml` and in the
   wizard's settings, because the resolver validates it from every source
   with one message and only for commands that connect, as Go does;
   `--template`, because `init` refuses an unknown name with exit 2 and
   `unknown template: <name>`, which clap would reword; and `--compat`,
   because deploy refuses it inside the deployer, so the refusal reaches
   the `--json` record with exit 10.
10. **Typed module errors and `conn::retry`.** Port groups 6 and 7.
    Done 2026-11-11: `ParseError`, `ResolveError` and `MetaError` as
    record 0002 now says; tests match variants instead of substrings
    where they branch. `error::chain` is the one flattener (`Error::text`
    and `deploy::sql_error` use it too). `conn::retry` holds
    `transient(Cause)` (a SQLSTATE or an `io::ErrorKind`) and `delay(n)`
    over the 100/200/400 ms table; `Dialer::connect` and the classifiers
    in `dial` call them, and the failure text is the pure
    `dial::connect_error`. Tests: the contract's transient list against
    Go's non-transient SQLSTATEs, the delay table, and the SQLSTATE and
    hint rules of `connect_error`. PGMI-866 added Go's transient
    broken pipe, early close (`UnexpectedEof`) and temporary name-lookup
    failure (`retry::transient_lookup`). Not ported from group 7: the
    `WrapConnectionError` texts that Rust words differently (`too many
    connections to database`, the pgpass and Azure hints); both are
    behaviour, not structure. The other `Result<_, String>` functions
    stay: no caller branches on them (`duration::parse` and `go_bool`
    feed clap, `tap` and `wire` are fuzzed leaves, the cloud helpers
    end as one connection error).
11. **One runtime.** `lib::run` owns it; `deploy::run`, `serve::run`,
    `tui::connection::run` take a handle.
    Done 2026-11-11: `lib::runtime` builds the multi-thread runtime;
    `deploy::run` and `serve::run` take it by value (they shut it down in
    their own order), `init` and `config` pass a `Handle` to
    `tui::connection::run`. Record 0003 has the details.

Separate tickets, in parallel after steps 3 and 7: port the remaining Go
unit groups (2, 3, 4, 10, 12, wizards; done 2026-11-11, PGMI-863, mapped in
`rust/docs/test-parity-units.md` with the parity fixes the ports forced); a fuzz/property target for
`conn::parse` (done 2026-11-11 with the lexer, splitter, metadata, `.env` and
`.pgpass` targets, PGMI-864 and PGMI-870: proptests in `invariants.rs`, one
cargo-fuzz target per parser, the CI job `fuzz` blocking); port the content linters and the template deployment tests
of `internal/scaffold` and `internal/services` to `rust/pgmi/tests/` before
switch day. That port landed 2026-11-11 (PGMI-862): linters in
`src/content_tests/`, deployments in `tests/`, mapped in
`rust/docs/test-parity-shared.md`.

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

### Lints (PGMI-869)

`[workspace.lints.clippy]` turns on `pedantic` (CI's `-D warnings` makes it
fatal) on top of `all`. The pedantic lints that fire are fixed, except
these, allowed workspace-wide:

| Lint | Why it stays allowed |
|---|---|
| `missing_errors_doc` | The library is pgmi's own; its `pub` surface exists for the fuzz targets (0001). |
| `too_many_lines` | Test bodies and the phase-ordered orchestrators; splitting `deploy` into one function per phase is review B's m6, not done in PGMI-869. |
| `items_after_statements` | Integration tests declare a helper next to the step that uses it. |
| `assert_is_empty` | `assert!(v.is_empty())` reads better than the `[] as [T; 0]` comparison the lint rewrites it to. |
| `case_sensitive_file_extension_comparisons` | Extensions compare case-sensitively, as in Go; changing it changes which files run. |
| `cast_precision_loss` | Duration rendering repeats Go's float arithmetic; tests compute ratios. |
| `unnecessary_wraps` | Test helpers return the `Option` the field under test holds. |
| `struct_field_names` | Field names follow the `--json`/MCP keys (`validationPassed`) and the flag groups. |
| `many_single_char_names` | The byte-level parsers (dotenv, MCP wire) use `i`, `j`, `b`, `c` cursors. |
| `struct_excessive_bools` | `DeployArgs` mirrors Go's boolean flags for clap. |
| `naive_bytecount` | One newline count in a test; not worth a dependency. |

### Review B (PGMI-858) items PGMI-869 did not take

- **m1** The remaining `Result<_, String>` (cloud providers, `serve::wire`,
  `project::ignore`, `duration`, the dialer's address lookup, the wizard's
  test) have no caller that branches on them; `conn::tap`, which had one,
  now returns `tap::Error`. A `cloud::Error` with `#[source]` would keep
  the reqwest chain but must render Go's texts unchanged; left for a
  change that touches the cloud providers.
- **m2** `Error::source()` is implemented; `Display` still renders the
  chain, because every printer, the `--json` record and MCP read
  `Error::text()` and moving them to `error::chain` changes no output but
  every call site. `ResolveError::Env(Error)` stays: the only env failure
  is a non-UTF-8 value, already a config error.
- **m4** `--params-file -` stays a string and `Context::stdin` a flag: the
  "stdin given N times" refusal and its text live in `params::collect`,
  as in Go, and an enum would only move that check.
- **m6** The deploy orchestrator is still one function; `too_many_lines`
  is allowed above. Splitting it into one function per phase is a
  separate change.
- **m10** The unported Go groups (`internal/cli/pgpass_test.go`,
  `internal/ui/approver_test.go`, `internal/cli/serve_*_test.go`) are test
  porting, PGMI-863's kind of work; listed in `rust/docs/test-parity-units.md`.
- **s1, s2, s3** `Progress`'s phase booleans, `Spec`'s TLS booleans and
  `Env`'s per-lookup allocation are left: each is a local change with no
  bug behind it. **s5** The DB tests still skip when `PGMI_TEST_CONN` is
  unset; CI sets `PGMI_REQUIRE_DB`, which turns a skip into a failure.
- Done elsewhere: m9 (`testdir::TempDir`, PGMI-870), m13 (CI lints the
  `testhooks` build and the fuzz crate).

## Consequences

- Every Go unit test whose subject exists in Rust gets a Rust counterpart
  reachable without a disk, server or terminal, except database behaviour,
  which moves to library-level integration tests.
- `fuzz/` links the library and `tests/` runs the binary; no file is
  compiled twice.
- 0001's module list, 0003's runtime note, 0017's blocking-pool note and
  0019's `DeployRecord` are brought in line with the code in the commits
  that change them.

## Serves

Engineering parity (owner decision 2026-11-11); Exit code table; serve;
Project discovery and loading; The --json record; Timeout, cancellation,
interruption.
