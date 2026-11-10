# 0003: Sync or async runtime

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: tokio 1.54.1, tokio-util 0.7.20,
tokio-postgres 0.7.18 (async only), azure_identity 1.0.0, aws-config 1.13.1,
google-cloud-auth 1.18.0 (all async only).

## Context

A deploy is one connection doing one thing at a time, so a synchronous
program would do. But the PostgreSQL driver chosen in spike A, and all three
cloud credential crates, are async. `pgmi serve` must answer `ping` while a
deploy runs and cancel a running deploy. Notices must stream while a
statement runs, and `--timeout` and SIGINT must cancel on the server.

## Options

1. **Synchronous code** (the `postgres` crate, blocking HTTP, threads for
   `serve`). `postgres` runs a hidden tokio runtime internally, and the cloud
   crates would each need one too, so this is async underneath with less
   control over cancellation.
2. **tokio, current-thread runtime.** One thread, no data races to think
   about, but CPU work such as loading and hashing 3000 files would stall
   `ping` in `serve` unless pushed to the blocking pool.
3. **tokio, multi-thread runtime.** The default, and what every dependency is
   tested on.

## Decision

Option 3: `tokio` with features `rt-multi-thread`, `net`, `io-util`,
`io-std`, `time`, `signal`, `sync`, `process`, `fs`, `macros`.

- `main` is synchronous. It parses arguments, builds the environment snapshot
  and exports the project `.env` (record 0016) while only one thread exists,
  then builds the runtime and `block_on`s the command.
- Cancellation is one `tokio_util::sync::CancellationToken` per deploy,
  fired by `--timeout`, SIGINT/SIGTERM (`tokio::signal`) or MCP
  `notifications/cancelled`. The deploy `select!`s on it and sends the
  server-side cancel (record 0004).
- File loading and hashing run in `spawn_blocking`.
- Wizards read keys with crossterm's `EventStream` inside the same runtime
  (record 0012).

## Consequences

- No `std::env::set_var` after the runtime starts (record 0016).
- One cancellation path serves CLI and `serve` alike.

## Serves

serve; Timeout, cancellation, interruption; Notices and the summary line;
Concurrency.
