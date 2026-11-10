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
   exactly as the cases say.
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

- When MCP adds a revision, pgmi changes its own code; rmcp stays the
  reference to compare against.

## Serves

serve; The --json record (deploy result); Timeout, cancellation,
interruption (cancel in `serve`); Concurrency (exit 15 between concurrent
calls).
