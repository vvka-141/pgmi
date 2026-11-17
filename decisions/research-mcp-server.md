# Research: the MCP server for the Rust binary

Date: 2026-11-10. Input to record 0017 (PGMI-829). Sources read: the rmcp
3.9.0 crate source from crates.io, the rust-sdk GitHub issues and PRs, docs.rs
through Context7, and the crate sources of rust-mcp-sdk 2.0.0 and pmcp 2.23.0.
No cargo build was run on this machine. The points marked "spike" are not
verified at runtime.

## The parity bar

Go's server is hand-written: `internal/mcp/protocol.go` (174 lines) and
`internal/mcp/server.go` (514 lines), about 690 lines with comments. The seven
tools and their schemas are a separate 691 lines in `internal/cli/serve.go`.
Both options need that second part, so only the 690 lines are in question.

What Go does (`internal/mcp/server.go`):

- Reads one line at a time with `bufio.Reader.ReadString` (line 102). There is
  no size cap. A last line without a newline is still dispatched.
- Text that is not JSON gets -32700. JSON that is not a request (a batch array,
  a scalar, a wrong field type) gets -32600. Both use `"id": null`, and the
  session goes on (lines 124-140).
- A bad `jsonrpc` value, or an id that is not a string or number, gets -32600
  with `"id": null`.
- Methods: `initialize`, `ping`, `tools/list`, `tools/call`, `server/discover`.
  The notifications `notifications/initialized` and `notifications/cancelled`
  are handled. Other notifications are ignored, and other requests get -32601.
- Versions: `initialize` echoes `2025-11-25`, `2025-06-18` or `2024-11-05`.
  Anything else, `2025-03-26` included, gets `2025-11-25`. A request whose
  `_meta` names `2026-07-28` is served statelessly: `resultType: "complete"`,
  the server info in `_meta`, -32022 for an unknown version, -32602 when the
  client capabilities are missing (`era`, line 312).
- Each `tools/call` runs in its own goroutine. `notifications/cancelled`
  cancels its context, and a cancelled call gets no answer (line 224).
- A panicking tool answers `isError: true` with its stack (line 436).
- End of input waits for running calls, then returns 0.

The black-box cases check only part of this. `serve/session` sends
`not json` and expects `"id":null,"error":{"code":-32700`. It also matches
field order with regexes, for example `"id":1,"result":\{"protocolVersion"`.
`serve/deploy-tool` needs a ping answered while a deploy runs. `serve/cancel`
needs a cancelled deploy to roll back. The contract (`docs/design/
behaviour-contract.md`, "serve") adds -32601 and exit 0 at end of input.

## 1. rmcp

**The claim is correct.** rmcp 3.9.0 (2026-11-05) skips a line that is not
JSON and sends nothing back. `src/transport/async_rw.rs:312-332`:

```rust
serde_json::error::Category::Syntax | serde_json::error::Category::Eof => {
    // ... replying to invalid data can trigger an error storm
    // ... This matches the other official MCP SDKs, which ignore unparsable input.
    // See https://github.com/modelcontextprotocol/rust-sdk/issues/938
    tracing::debug!("Ignoring unparsable incoming message: {e}");
}
```

The test `receive_ignores_parse_error` (line 836) checks that nothing comes
back. JSON with the wrong shape does get an answer: -32600, but with no `id`
member at all (`JsonRpcError.id` is `skip_serializing_if = "Option::is_none"`,
`src/model.rs:594-601`, citing the 2026-07-28 spec). Go sends `"id": null`.

**It is a deliberate choice, and it was reversed once.**

- Issue #825 (2026-04-30) reported that a bad line closed the stream. PR #833
  by maintainer DaleSeo made it answer -32700 (merged 2026-05-07).
- Issue #938 (2026-06-28, by stephentoub, who maintains the C# SDK) asked to
  stop answering. The reason was an error storm with a client that logs to
  stdout. PR #940 removed the answer (merged 2026-07-01).
- Open issue #1395 (2026-10-26) asks for an answer to an over-long request. It
  says plainly that non-JSON input should stay unanswered.

So no fix is coming. It is not configurable. JSON-RPC 2.0 says a server MUST
answer a parse error with -32700. The MCP stdio transport says nothing about
it. Go follows JSON-RPC, and rmcp follows the other SDKs. The claim that the
other official SDKs ignore bad input comes from #938. I did not check it.

**pgmi can wrap it, through a supported public API.** The transport is
pluggable. Any `(Sink<TxJsonRpcMessage<RoleServer>>,
Stream<Item = RxJsonRpcMessage<RoleServer>>)` pair is a transport (docs.rs
`rmcp::transport::sink_stream`, `src/transport.rs` module docs). pgmi would
not use rmcp's `AsyncRwTransport` at all. Its own stream reads lines, sorts
each into a message, a -32700 or a -32600, and writes the error lines itself
with `"id": null` through a writer shared with the sink. Estimate: 100-150
lines plus tests.

0017 also says pgmi would have to override the version negotiation. That
override is one documented method, `ServerHandler::supported_protocol_versions`
(`src/handler/server.rs:448`). Leaving out `2025-03-26` and setting the
server's version to `2025-11-25` gives Go's answers
(`negotiate_protocol_version`, `src/service/server.rs:478`). rmcp already
handles `server/discover`, -32022, `resultType` and the request `_meta`.

rmcp also gives what the tests need. Each request runs in its own spawned task
(`src/service.rs:1563-1600`), so a ping is answered during a deploy.
`notifications/cancelled` cancels that request's token (`src/service.rs:1664`).
An unknown method is a `CustomRequest`, which answers -32601. The response
structs keep field order: `jsonrpc, id, result`, and `protocolVersion` first.
Spike: whether a cancelled call still gets an answer, whether a ping in the
initialize era answers exactly `{}`, and how a panic in a handler is reported.
pgmi would wrap each tool in `catch_unwind` either way.

**Maturity.** It is the official SDK (`modelcontextprotocol/rust-sdk`, 4.1k
stars). It is Apache-2.0 on crates.io. MSRV is 1.88, below the 1.94 that the
AWS crates already need (0008). It has 47M downloads. The main maintainers are
DaleSeo, alexhancock, 4t145, jokemanfire and jamadeo. The churn is high:
1.0.0 came out on 2026-03-03, 2.0.0 on 2026-06-29 and 3.0.0 on 2026-07-28.
There have been 17 releases since 3.0.0. It knows protocol versions
`2024-11-05` through `2026-07-28`.

**Dependency weight.** The required dependencies are futures, indexmap,
pin-project-lite, serde, serde_json, sha2, thiserror, tokio, tokio-util,
tracing and chrono. The `server` feature adds schemars, uuid and pastey, and
the defaults add the rmcp-macros proc-macro. tokio, serde_json and sha2 are
already in the build (0003, 0015). The new ones are chrono, schemars, uuid,
indexmap, tracing and the macros. That is moderate, not heavy. Not measured
with `cargo tree`.

## 2. Alternatives

| Crate | Version, date | Bad line | Notes |
|---|---|---|---|
| rust-mcp-sdk | 2.0.0, 2026-08-27, MIT | Dropped without an answer (`rust-mcp-transport/src/mcp_stream.rs:141-146`, `Err(_) => continue`) | Community project, about 114k recent downloads, MSRV 1.80, schema from rust-mcp-schema |
| pmcp | 2.23.0, 2026-10-28, MIT | The stdio receive fails, and the server loop stops on a receive error (`src/server/mod.rs:1517`). That looks worse than dropping the line. | One-company project, MSRV 1.91 |
| turbomcp | 4.0.0-alpha.5, MIT | Not checked | Alpha |
| mcp-attr, mcpr, ultrafast-mcp, mcp-protocol-sdk | last release in 2025 | Not checked | Abandoned or tiny |

None of them answers -32700 out of the box. None beats rmcp on maturity or
spec coverage. They are not worth pursuing.

## 3. The two real options

**A. rmcp with pgmi's own stdio transport.** About 150 lines of transport, about
150 lines of `ServerHandler` (version list, `call_tool` dispatch to the seven
tools, panic capture), and the tools. New MCP revisions arrive with an rmcp
upgrade. The cost is a fast-moving dependency (three majors in eight months)
whose defaults differ from Go in small ways: the missing `id` on its own
errors, response caching and subscription logic pgmi does not use, and
whatever the next release changes in the serialized output. Each of those
needs a test to pin it, because the black-box regexes depend on bytes.

**B. Hand-written, a port of the Go server.** Estimate: 650-850 lines of
Rust. That covers serde types (~200), the line loop with a reader task, a
writer task and dispatch (~250), the cancellation map (~60), `initialize`
negotiation and the 2026-07-28 era (~150), and error and panic results (~80).
Port Go's tests too: `server_test.go` and `server_modern_test.go` hold about
1,000 lines of cases. It must cover `initialize` and version echo,
`notifications/initialized`, `ping`, `tools/list`, `tools/call`,
`notifications/cancelled` with no answer after a cancel, `server/discover` and
the `_meta` era, and the -32700, -32600, -32601, -32602 and -32022 errors with
Go's id rules.

Testing for B:

- the three black-box cases, unchanged;
- the ported Go unit cases;
- proptest on the line decoder: random byte lines mixed with valid frames.
  Every valid request gets exactly one answer with its id, every bad line gets
  exactly one error with `"id": null`, and nothing panics;
- a cargo-fuzz target on the same decoder (nightly, run outside CI);
- rmcp as a dev-dependency only. A test drives `pgmi serve` with rmcp's
  client. That catches drift from the official SDK without shipping it.

What B misses: a new MCP revision is pgmi's own work, as it was in Go for
2026-07-28. Resources, prompts, sampling and elicitation are not there. pgmi
uses none of them.

## 4. Recommendation

**Keep option B, the hand-written server, but fix 0017's reasoning.** Two of
its arguments are wrong. A custom transport is a supported public API and
about 150 lines. Narrowing the versions is one method override, not a
replacement. The real reasons for B are these:

- The parity bar is an existing 690-line Go server with about 1,000 lines of
  tests. Porting it is mechanical and low-risk.
- pgmi uses a tiny part of MCP: seven tools over stdio.
- rmcp's three majors in eight months and its different error-id habit would
  cost steady upkeep to keep byte-level parity.
- rmcp has deliberately taken the opposite side on the very behaviour the
  contract requires.

Residual risks:

1. MCP revisions come about twice a year. Each is pgmi's own work. The rmcp
   client dev-test shows when pgmi falls behind.
2. Go has no line-length cap. Matching that keeps an out-of-memory risk from a
   client that never sends a newline. Decide whether parity or a cap (for
   example 16 MiB, answering -32600) wins, and write it into the contract.
3. The error-storm argument in #938 is real for clients that echo errors.
   Go has the same exposure, and no report of it has come in.

Fallback: if the hand-written server grows past about 1,200 lines, or the next
MCP revision is large, switch to option A. Its transport (about 150 lines) can
be written first as a spike to confirm that the three black-box cases pass
under rmcp.
