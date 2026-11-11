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
  then exits 0. SIGINT cancels running calls first.
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
- The server is `serve::serve(input, output, toolbox, stop)` over any
  `AsyncRead`/`AsyncWrite` (record 0022 step 7); `pgmi serve` passes
  stdin, stdout, pgmi's `Tools` and a token its signal task cancels. The
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
- The writer task watches the session (PGMI-868): a failed write cancels
  the session token, which stops the reader and every call; after the
  reader ends, the remaining answers get 2 s (`DRAIN`), so a client that
  keeps stdout open but stops reading cannot hold up the exit.
- Bounds (PGMI-868, decided changes in the behaviour contract): a line of
  at most `wire::MAX_LINE` (16 MiB; `Lines` drops a longer one without
  buffering it and counts it, the reader answers -32600), `MAX_CALLS` (32)
  tool calls running (the next is -32000), and `QUEUED_ANSWERS` (1024) in
  the bounded answer channel. The reader never waits on that channel: when
  it is full, the client is not reading and the session ends, so
  cancellations are always read. A call waits for room, racing the session.
- `init`, `metadata_plan`, `metadata_validate` and `ai_contract` answer
  "not implemented in this build" (exit 1) until the commands they wrap land
  (PGMI-833 part B); their argument checks already match Go.

## Serves

serve; The --json record (deploy result); Timeout, cancellation,
interruption (cancel in `serve`); Concurrency (exit 15 between concurrent
calls).
