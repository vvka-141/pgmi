# Rust dependency risk summary

Date: 2026-11-10. Ticket: PGMI-829, at the owner's request. Versions and
dates from crates.io on that date. Details: `research-postgres-driver.md`,
`research-mcp-server.md` and records 0004-0009, 0017.

The Go binary leans on pgx, the official Azure, AWS and Google Go SDKs and Go's
own TLS. Each is mature and covers what pgmi needs. In Rust, the equivalents are
younger, so each area below has a known gap. Every gap has a way out that is
already chosen. None of them needs a fork, and no way out depends on someone
else's release.

| Area | Crate (version, last release) | Maturity | Known gap | What pgmi does | Way out if it fails |
|---|---|---|---|---|---|
| PostgreSQL driver | tokio-postgres 0.7.18 (2026-06-12) | The standard async driver, 26 M recent downloads; one maintainer releases every 2-5 months | No transaction status and no `require_auth`; connection options are basic (no multiple hosts, verify-ca/full, `.pgpass`) | Own connection resolution on `connect_raw` (spike A: 25 of 29 cases first try); a 195-line layer on its own connection reads the status byte and enforces `require_auth`, no fork (owner, 2026-11-10) | A fork, with the status patch already written in spike A. Alternatives checked and rejected: sqlx (no notices, cancel, several hosts), libpq (OpenSSL and C in every build), pgwire (client unfinished) |
| MCP server | none: hand-written, as in Go | Go's is about 690 lines of pgmi's own code, with about 1,000 lines of tests | rmcp 3.9.0, the official SDK, drops unparsable lines on purpose (removed its -32700 answer in July 2026) and shipped three major versions in eight months | Port Go's server, about 650-850 lines; black-box cases, a property test and a fuzz target on the line decoder, and rmcp's client as a test-only conformance check | rmcp with pgmi's own stdio transport (public API, about 150 lines) restores the -32700 answer |
| TLS | rustls 0.23.46 (2026-10-14) on ring 0.17.14 (2025-03-11) | rustls is the standard Rust TLS stack; ring is used by most of the ecosystem | ring has had no release in 20 months. Its maintainer stepped back in 2025 and the rustls team keeps it working | ring, because it cross-builds to all six targets without a C toolchain (spike D). CI bans OpenSSL and aws-lc-rs so nothing drags them in by accident | Switch the rustls provider to aws-lc-rs: one line in pgmi plus a C toolchain in the release build. aws-lc-rs is maintained by AWS |
| Azure sign-in | azure_identity 1.0.0 (2026-11-06) | 1.0 four days ago; Microsoft's official SDK | No default credential chain; managed identity covers only App Service and VMs; the certificate credential needs OpenSSL | pgmi builds the chain (as Go does), adds Cloud Shell, Arc, Service Fabric and Azure ML managed identity, and signs certificate logins itself (about 100 lines, Microsoft's documented format) | The flows are plain HTTPS and JSON; any one that breaks can be written by hand from Microsoft's protocol docs. The real-Azure runs (PGMI-845) prove each route before switch day |
| AWS sign-in | aws-sigv4 1.6.5 and aws-config 1.13.1 (2026-11-09) | AWS's official SDK, released weekly | None for pgmi: the token is a SigV4-signed URL | Sign the RDS token with aws-sigv4 directly, credentials from aws-config | Signing is a published algorithm; pgmi's black-box case pins the token format |
| Google sign-in | google-cloud-auth 1.18.0 (2026-10-21) | Google's official SDK, monthly releases | No Rust Cloud SQL connector from Google; the community crate does not build on Windows; the SDK links Apple frameworks, so macOS builds on a Mac | Own Cloud SQL dialer (about 500 lines, spike B); Google's crate only for credentials | Credentials can fall back to service-account keys and the metadata server by hand; the macOS fallback is the owner's (drop that build, record 0018) |

## What the Go version did not have to deal with

- **Driver:** pgx covers every libpq behaviour pgmi relies on. tokio-postgres
  does not, so about 2,000 lines of connection code move from the library into
  pgmi. That code is pinned by the 41 driver rows of the black-box suite, which
  Go also passes.
- **Cloud SDKs:** Go's are years old; Azure's Rust SDK reached 1.0 this month.
  Real-cloud proof is Azure only (owner decision), so AWS and Google rest on
  fakes.
- **TLS:** Go ships its own TLS. Rust needs a crypto provider, and the one that
  cross-builds cleanly (ring) is quiet.

## Recommendation

1. **Driver (accepted 2026-11-10):** upstream tokio-postgres with the layer,
   no fork, nothing offered upstream; the layer is permanent. The spike passed
   the six cases that need it. Done only with a `pgmi serve` case, a fuzz
   target and TLS start from sslmode (record 0005).
2. **MCP (accepted 2026-11-10):** hand-written, as in Go, with rmcp as a test-only conformance check
   and the rmcp-plus-own-transport route as the fallback.
3. **TLS:** ring now; aws-lc-rs is the planned exit, tested once in the release
   build during PGMI-834 so the exit is known to work.
4. **Cloud:** as records 0007-0009, each proven by black-box fakes, Azure also
   by the real runs.
