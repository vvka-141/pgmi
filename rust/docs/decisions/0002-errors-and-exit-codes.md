# 0002: Error model and exit codes

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: thiserror 2.0.21, anyhow 1.0.104 (both
considered, neither chosen for the top-level type); libc (for SIGPIPE).

## Context

Every failure maps to one exit code (0, 1, 2, 3, 10–16, 130), and `--json`
reports the same `exitCode`. A Rust panic exits 101 by default; pgmi must exit
3. The SQL failure path also carries diagnostics (`sqlstate`, `failedFile`,
`line`, ...). `pgmi serve` must turn a failing or panicking tool into
`isError` with the exit code, and keep running.

## Options

1. **`anyhow::Error` everywhere,** with the exit code found by downcasting at
   the top. Easy to write, but the code is decided far from where the failure
   is understood, and a forgotten downcast silently becomes exit 1.
2. **One `thiserror` enum per module,** converted at boundaries. Precise, but
   many conversion impls for a solo codebase.
3. **One concrete `pgmi::Error`** that carries its kind from the moment it is
   created, plus an optional source chain and optional SQL diagnostics.

## Decision

Option 3.

```rust
pub enum Kind { General, Usage, Internal, Config, Connection, Denied,
                Sql, NoDeploySql, Locked, Timeout, Interrupted }
pub struct Error {
    kind: Kind,
    message: String,
    source: Option<Box<dyn std::error::Error + Send + Sync>>,
    sql: Option<Box<SqlDiagnostics>>,
}
```

- `Kind::exit_code()` is the only place the numbers live (1, 2, 3, 10, 11, 12,
  13, 14, 15, 16, 130). Constructors read as the contract does:
  `Error::config("...")`, `Error::connection(...)`.
- Precedence when causes combine: an interruption (SIGINT/SIGTERM) is 130
  even if the deploy had already failed or succeeded; a `--timeout` that runs
  out while connecting is 11, after connecting 16 (record 0004).
- stderr prints `pgmi: error: <message>: <cause>: <cause>`; the chain is
  printed in full, with secrets redacted (record 0019).
- Library errors (tokio-postgres, rustls, cloud SDKs, io) are wrapped where
  they happen, with the kind that call site knows (a rustls handshake error
  while connecting is `Connection`; a wrong `sslpassword` is `Config`).
- **Panics.** `main` runs everything inside `std::panic::catch_unwind`; a
  panic hook records the message and a forced backtrace. A panic ends as
  `Kind::Internal`, exit 3, and with `--json` the record still prints
  (`status: failed`, `exitCode: 3`). A panic inside a tokio task arrives as a
  `JoinError` and gets the same mapping. In `pgmi serve` the tool answers
  `isError` with the stack in `structuredContent` and the session goes on.
  The release profile keeps `panic = "unwind"`; `abort` would bypass all of
  this.
- **Broken pipe.** Rust ignores SIGPIPE, so `pgmi ai | head` would panic
  inside `println!`. On Unix `main` restores the default SIGPIPE action
  first, so pgmi dies quietly as Go does. Everything writes through
  `writeln!`, never `println!`.
- A cargo feature `testhooks`, off in releases, adds a `PGMI_TEST_PANIC`
  trigger so an integration test proves the panic path exits 3. A release
  binary cannot panic on purpose, as the contract says.
  As built (PGMI-833): the trigger fires in deploy after connection
  resolution; `tests/panic.rs` runs in CI as
  `cargo test -p pgmi --features testhooks --test panic`. SIGPIPE is restored
  with `libc::signal` in a second justified `unsafe` block in `main`.

## Consequences

- Every new failure site picks a kind on purpose; review checks it.
- No `anyhow` in the binary. `thiserror` may appear inside a module whose
  errors have structure (pgpass parsing), converted at its boundary.

## Serves

Exit code table; serve; Error attribution; Timeout, cancellation,
interruption; Google sign-in failure is exit 11.
