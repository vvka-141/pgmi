# 0017: The MCP server (`pgmi serve`)

Status: accepted by the owner, 2026-11-10, with three conditions (below).
Ticket: PGMI-829. Research: `research-mcp-server.md`.
Checked on crates.io and in the published source, 2026-11-10: rmcp 3.9.0
(2026-11-05, the official Rust SDK). It knows protocol versions up to
`2026-07-28` and `server/discover`. Its stdio transport skips a line it
cannot parse without answering (`src/transport/async_rw.rs`, test
`receive_ignores_parse_error`), and it accepts `2025-03-26`. serde_json
1.0.154, tokio-util 0.7.20.

## Context

`pgmi serve` speaks JSON-RPC 2.0, one message per line on stdio, with the
behaviour in `docs/CLI.md` "pgmi serve": seven tools with output schemas and
annotations; a malformed line answers -32700 with `"id": null` and the
session goes on; an unknown method is -32601; `ping` is answered during a
deploy; `notifications/cancelled` stops a deploy, which rolls back, and gets
no response; end of input exits 0; a panicking tool answers `isError` with
its stack. It serves both the 2026-07-28 stateless era (`server/discover`,
`-32022`, `resultType: "complete"`) and the earlier `initialize` era,
echoing `2025-11-25`, `2025-06-18` or `2024-11-05` but never `2025-03-26`.

## Options

1. **rmcp.** Current, and it already knows the 2026-07-28 era. But its
   transport drops unparsable lines silently, which breaks the -32700
   behaviour the `serve/session` case checks, and pgmi would override its
   version negotiation too. Replacing transport and negotiation leaves
   little of the SDK in use, while keeping its dependencies (chrono,
   schemars and more).
2. **Hand-written**, as the Go server was: about 700 lines of serde types,
   a dispatcher and the seven tools calling the same functions the CLI
   calls.

## Decision

Option 2, hand-written like the Go server. It counts as done only when all
three hold (checklist on PGMI-833, where `pgmi serve` is built):

1. It matches the Go server's behaviour, and black-box cases cover that
   behaviour, malformed input included: invalid JSON, a non-object, a
   missing or wrong `jsonrpc`, a bad `id` type, unknown methods, wrong
   params, an oversized line, a blank line, invalid UTF-8. Each case passes on
   the Go binary first.
2. A fuzz target (cargo-fuzz) on the input parser: the line reader and the
   message decoder never panic, and every line gets one answer or none,
   exactly as the cases say. Met: `mcp_decoder` runs for two minutes on
   every push in the CI job `fuzz` (PGMI-861).
3. The fallback below stays a fallback.

## Fallback only

rmcp, the official SDK, with pgmi's own stdio transport (about 150 lines on
its public API) to restore the -32700 answer and its version negotiation
overridden. Used only if the hand-written server proves unworkable.

- A reader task reads lines; each parse failure answers -32700 with
  `"id": null`. Requests are dispatched by method. Each `tools/call` runs as
  its own tokio task with a `CancellationToken` kept in a map by request id;
  `notifications/cancelled` fires it. A writer task owns stdout and writes
  one message per line, flushed, so concurrent answers never interleave.
- Field order is fixed by struct order (`jsonrpc`, `id`, `result`/`error`;
  `protocolVersion` first in the `initialize` result), which the case
  regexes rely on.
- At end of input pgmi stops reading, waits for running calls to answer,
  then exits 0. SIGINT cancels running calls first. `serve` returns why the
  session ended (`Ended`: end of input, stopped, slow reader, read failed,
  write failed); `run` maps a failed read or write to exit 1, as Go's
  returned error is, and the rest to 0.
- Tool panics come back as `JoinError`; the panic hook's captured stack goes
  into `structuredContent` (record 0002).
- `deploy` runs the CLI's deploy function with the arguments mapped to flags,
  no `.env`, `overwrite` requiring `confirmDatabaseName` (exit 12 before
  connecting), its notices collected per call (record 0019).
- Output schemas are hand-written JSON next to the result types, with a unit
  test that each result validates against its schema.

## Consequences

- Condition 1's cases exist (PGMI-830) and pass on Go: `serve/malformed`,
  `serve/protocol-versions`, `serve/deploy-guards`, `serve/deploy-timeout`,
  `serve/deploys-in-sequence`, `serve/concurrent-deploys`. Two Go answers
  are left unpinned and are owner questions in `go-review.md`: the bare
  line `null` gets no answer (JSON-RPC says -32600), and an id holding
  invalid UTF-8 is echoed as raw bytes, which makes the response invalid
  JSON. serde_json refuses invalid UTF-8, but `serve/malformed` requires
  invalid UTF-8 inside a string to read as U+FFFD (Go does), so the reader
  decodes each line lossily before parsing; that also makes the second
  answer valid JSON.
- Deploys on one serve process are independent: each opens and closes its
  own connections, so a deploy that ended inside an open transaction leaves
  no lock behind (`serve/deploys-in-sequence`); keep that, never share a
  connection between calls.

- When MCP adds a revision, pgmi changes its own code; rmcp stays the
  reference to compare against.

## As built (PGMI-833)

- `serve/wire.rs` is the input parser: a line splitter, a byte-level JSON
  validator that gives Go's `encoding/json` messages (`invalid character
  '\xff' looking for beginning of value`, `unexpected end of JSON input`),
  per-byte U+FFFD decoding inside strings, and Go's struct decoding rules
  (case-insensitive member names, last duplicate wins, `null` leaves a field
  unset, the first type error is reported). It depends on std and serde_json
  only; the cargo-fuzz target `mcp_decoder` links it as `pgmi::serve::wire`
  (record 0022), and a proptest in `cargo test` checks the same properties.
- The server is `serve::serve(input, output, backlog, toolbox, stop)` over
  any `AsyncRead`/`AsyncWrite` (record 0022 step 7); `pgmi serve` passes
  stdin, stdout, stdout's `Backlog`, pgmi's `Tools` and a token its signal
  task cancels. The
  protocol behaviour is unit-tested in-process over `tokio::io::duplex`
  with a fake toolbox, as Go's `Serve(ctx, in, out)` tests are.
- Ids are echoed as written (`1e2`, `-0`), through serde_json's `raw_value`.
- A bare `null` line gets no answer, as in Go (owner question Q1 still
  open). A request whose method starts with `notifications/` is never
  answered, with or without an id, as in Go.
- `tools.json` is the tool list as Go's `tools/list` prints it (descriptions,
  input and output schemas), so the 2025 era answer is byte-identical; the
  2026-07-28 era parses it and sorts keys as Go does. A unit test checks a
  deploy record and a failure against the output schema's required keys.
- Failures before the deployer runs (arguments, path, timeout, params,
  database, `confirmDatabaseName`) answer `{message, exitCode}`; later ones
  answer the `--json` record plus `message`, as Go does.
- Each tool call runs on its own `tokio::spawn` task (PGMI-869). A panic
  arrives as the `JoinError` and answers `isError`, exit code 3, with the
  hook's stack. `deploy` runs on the runtime; the other tools walk the disk
  synchronously and run under `spawn_blocking`. Until PGMI-869 each call ran
  on the blocking pool under `Handle::block_on`, because the deploy future was
  not `Send`. This record blamed the trait solver (rust-lang/rust#64552,
  #110338); the causes were ours. `Approver` was `#[async_trait(?Send)]`
  without a `Send + Sync` bound, so `&Context` was `!Send`. And the socket was
  a `Box<dyn Io>`: tokio-postgres's TLS handshake and cancel futures hold
  `<Connect as TlsConnect<S>>::Stream`, and inside a generator the trait
  object's lifetime is erased, so rustc looked for `TlsConnect<Box<dyn Io +
  '0>>` for every `'0` while `Connect` implements it only for `'static`. The
  socket is now an enum (`Tcp`, `Unix`, `CloudSql`) with no lifetime, and the
  dozen borrow sites the earlier attempt chased were symptoms of that one.
- `serve()` joins two named halves (fix round 4): `Reader` (reads lines,
  answers what needs no tool, starts a task per call, then waits for every
  call) and `writer`. The writer writes each answer as one line in flushed
  slices of 512 bytes and counts the client's progress in a `watch`
  channel. tokio's `Stdout` copies up to 2 MiB into its own buffer per
  write, so a `write` returning is no evidence the client read anything; a
  flushed slice is. A slice is not enough on its own (fix round 6): a
  Linux pipe frees room a whole page at a time, so a write waits until the
  reader has taken a full page, and a reader taking 1 KB/s through a
  one-page pipe made no visible progress for 4 s. While a slice waits, the
  writer therefore looks every 100 ms at the output's `Backlog`, the bytes
  written that the client has not read yet, and counts any change as
  progress. On Linux stdout's backlog, when stdout is a pipe, is
  `FIONREAD` on a duplicate of its descriptor (`rustix`, pure Rust): any
  byte the client takes changes it. Elsewhere (a socket, which is what a
  Node client's stdio is, a file, macOS, Windows) no call reports a
  writer's unread bytes, so progress is a written slice: a client reading
  less than `SLICE` per `DRAIN` (256 bytes a second) there can be dropped
  once its output buffer is full. A Unix socket frees a write's room only
  when the reader has taken all of it, so the small slice also keeps that
  granularity at 512 bytes. `InputEnd`,
  marked by the reader when it stops reading for any reason, is the one
  end-of-session state between the halves. Until then the writer waits on
  the client as long as the client takes; after it, the client must make
  progress within every `DRAIN` (2 s). A failed write cancels the session token,
  which stops the reader and every call. So a batch client that reads
  slowly but steadily after closing its input gets every answer, each a
  complete line, and a client that never reads ends 2 s after its input.
  The tests that pin this run the real binary over OS pipes shrunk to one
  page (`tests/serve_pipes.rs`: readers at 256 B/s, 1 KB/s, 8 KB/s and
  800 KB/s) and, in-process, a `StdoutLike` transport that buffers as
  tokio's `Stdout` does and a reader slower than a line per `DRAIN` with
  and without a backlog; the duplex tests with tiny lines missed it three
  times.
- Bounds (PGMI-868, decided changes in the behaviour contract): a line of
  at most `wire::MAX_LINE` (16 MiB; `Lines` drops a longer one without
  buffering it and counts it, the reader answers -32600), `MAX_CALLS` (32)
  tool calls (the next is -32000; counted by a semaphore whose `Slot` is
  shared: the call's task holds it until the answer is queued, the work
  until it really ends), and `QUEUED_ANSWERS` (1024) in the bounded answer
  channel. A full channel does not mean the client stopped reading: the
  reader answers a whole 64 KiB chunk at once, so a reading client that
  pipelines a burst fills it before the writer runs. Every answer, the
  reader's and a call's, enters through `Server::queue`, which waits for
  room as long as the client makes progress and ends the session
  (`SlowReader`, exit 0) after `DRAIN` without one. A slow reader behind a
  3.5 MB answer therefore keeps its session, and a client that keeps
  sending calls and never reads is dropped: at most 1024 answers and 32
  calls are held (before fix round 5 a finished call released its slot and
  waited to queue unbounded, about 41 KB per call). While the reader waits
  it reads nothing, so a `notifications/cancelled` behind the burst is read
  once the client takes an answer, or never if it takes none.
- Signals: the first SIGINT or SIGTERM cancels the session (answers already
  made are still written while the client takes them); the second exits
  130 at once, as `deploy` does (fix round 5, owner decision; Go ignores it).
- Blocking work keeps its call's place until it ends (fix round 6). The
  server runs each call inside `blocking::Slot::hold`, a task-local, and
  every blocking piece of pgmi starts through `blocking::spawn` (clippy's
  `disallowed-methods` refuses a bare `spawn_blocking`), which moves a
  clone of the running call's `Slot` into its closure. That covers the
  blocking tools (`init`, `metadata_plan` and the rest that answer from
  files) and every blocking step of a `deploy`: reading `pgmi.yaml`, the
  params files, the project, `.pgpass`, the TLS files and the cloud keys.
  A cancelled call stops waiting for its thread at once, so a stalled
  filesystem cannot hold up the end of a session, but its slot stays
  taken until the thread ends, so cancelling cannot pile up threads beyond
  32 calls; before, a cancelled `deploy` freed its slot while its
  `paramsFile` read went on, and 40 cancels left 40 threads. A params file
  is read through the `Vfs` seam (`read_named`) up to
  `PGMI_MAX_FILE_SIZE`; a named pipe or a device is read by polling every
  100 ms, so a cancel ends the wait (Unix). Opening a named pipe that no
  writer has opened blocks, as Go's `os.ReadFile` does, and keeps the
  call's slot until a writer comes. `init`, which writes, is never
  abandoned short of a second signal: a cancelled `init` call still waits
  for its thread, so the runtime's 100 ms shutdown never cuts a project
  half-written; a second SIGINT or SIGTERM exits 130 at once, whatever is
  running.
- On Unix a client that closes stdout kills serve with SIGPIPE, as it does
  Go's: `main` keeps the signal's default action (record 0002), so a closed
  pipe never reaches `Ended::WriteFailed`; other write errors exit 1.

## Serves

serve; The --json record (deploy result); Timeout, cancellation,
interruption (cancel in `serve`); Concurrency (exit 15 between concurrent
calls).
