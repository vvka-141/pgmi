# 0005: tokio-postgres unpatched, with a layer on pgmi's own connection

Status: accepted by the owner, 2026-11-10, with three conditions (below).
Ticket: PGMI-829. Replaces the first draft of this record (a published fork,
`pgmi-tokio-postgres`), which the owner did not approve. Research:
`research-postgres-driver.md`. Checked on crates.io, 2026-11-10:
tokio-postgres 0.7.18 (2026-06-12).

## Context

pgmi needs two things tokio-postgres 0.7.18 does not offer:

- **Transaction status** after each statement (the ReadyForQuery byte: idle,
  in a block, or in a failed block). pgmi uses it to count committed units,
  to report "deploy.sql ended inside an open transaction", and to roll back
  before releasing the deploy lock without a stray WARNING.
- **`require_auth`**: refuse an authentication method the user did not allow,
  before any password is sent.

## Options

1. **A fork**, published as `pgmi-tokio-postgres` (cargo ignores `[patch]`
   when building a published crate). Two small patches, but a crate to
   publish, keep in sync and retire.
2. **Upstream, unpatched, plus a layer on pgmi's own connection.**
   `connect_raw` takes any stream, and pgmi already opens the socket and
   supplies the TLS adapter. A wrapper on that stream reads the protocol's
   message headers as they arrive: it records each ReadyForQuery status byte,
   and checks each authentication request against `require_auth`, failing
   the read before tokio-postgres sees a refused request.
3. **Wait for upstream.** Neither feature has ever been asked for upstream;
   `require_auth` is unlikely ever to land.
4. **Another driver.** sqlx, libpq and pgwire were surveyed and are worse
   (research file).

## Spike (2026-11-10, branch `feature/pgmi-829-wire-tap-spike`)

The layer was built on spike A's prototype (`rust/spikes/connection/`),
against tokio-postgres 0.7.18 from crates.io, with no `[patch]` and no fork.

- **The layer:** `rust/spikes/connection/src/tap.rs`, 195 non-blank lines
  without tests: the frame reader (about 50), the `require_auth` policy (about
  50), the stream and TLS wrappers (about 95). Three unit tests, one of which
  feeds the same messages split at every possible byte boundary.
- **Supporting spike code, not part of the layer:** a minimal head/tail
  splitter (`src/split.rs`, 68 lines), the deploy lock and the unit loop in
  `main.rs` (55 changed lines). The real splitter is record 0014's lexer.
- **The six cases:** all pass: `execution/atomic-head`,
  `execution/open-transaction`, `execution/tail-autocommit`,
  `execution/tail-block-fails`, `exit-codes/15-concurrent-deploy`,
  `tls/require-auth-and-version` (the last failed in spike A).
- **No regression:** of the 34 connection, TLS, cancel, notice and execution
  cases, 28 pass. The six failures are known and not the driver's: three
  open since spike A (two contract decisions since settled, one unimplemented
  key format) and three that need the loader or the test macro, which the
  spike does not have.
- **The layer is load-bearing:** a build whose status always reads "idle"
  fails `execution/open-transaction`; a build that ignores `require_auth`
  fails `tls/require-auth-and-version`.

## Decision

Option 2: tokio-postgres from crates.io, unpatched, plus the layer. No fork.

The layer counts as done only when all three hold (checklist on PGMI-832, the
first ticket that builds the real connection code):

1. A black-box case for `pgmi serve`: several deploys on one connection, where
   a deploy that leaves a transaction open is detected and handled, and the
   next deploy on the same connection is not refused.
2. A fuzz target on the server-message reader. Met: `tap_reader` runs for
   two minutes on every push in the CI job `fuzz` (PGMI-861).
3. TLS start taken from the resolved sslmode, not from watching the wire for
   pgmi's own SSLRequest.

The layer is the permanent solution. pgmi offers nothing upstream (owner,
2026-11-10: a solo project takes on no upstream discussions).

## Fallback only

If the layer ever fails, the fork comes back. Two patches in `patches/`,
kept as reference only:

- `fork-transaction-status-0.7.18.patch`: spike A's patch, adding
  `Client::transaction_status()` to tokio-postgres 0.7.18 in three files.
- `transaction-status-master.patch`: the same feature against
  rust-postgres master of 2026-11-10 (81 lines, a `TransactionStatus` enum,
  also in the `postgres` crate, with a test that passed the crate's suite).

A fork would be published as its own crate, since cargo ignores `[patch]` in
a published crate. `require_auth` has no patch; the layer's policy half would
move into the fork's `connect_raw`.

## Consequences and risks

- **No driver internals.** The layer uses only public API: `connect_raw`,
  and the `TlsConnect` and `TlsStream` traits. It assumes two things that
  are true of the protocol rather than of the crate: the driver reads only
  through the stream it was given, and pgmi sends one query at a time, so
  the status byte stored when the read arrives belongs to the query just
  awaited.
- **A future tokio-postgres 0.8** could change the `TlsConnect` trait or
  `connect_raw`. The layer would need adapting, as any TLS adapter would.
- **A parsing bug fails silently:** a wrong status gives a wrong
  `unitsCommitted` or a missed rollback. Mitigation: the split-at-every-byte
  test, a fuzz target on the frame reader, and the black-box cases.
- **SSL negotiation:** the spike recognises pgmi's own SSLRequest on the
  socket to know when TLS starts. The real implementation takes this from
  the resolved sslmode (condition 3).
- **Test gap found:** `execution/tail-block-fails` and
  `exit-codes/15-concurrent-deploy` also pass without the status, because
  the process exits and the server then drops the lock. The status matters
  in a long-lived process: `pgmi serve` runs several deploys on one session
  (condition 1).
- No fork to publish: record 0018 loses a crate; record 0001 loses
  `rust/vendor/`; record 0006 adapts tokio-postgres-rustls to upstream.

## Serves

Execution contract (transaction status); Concurrency (rollback before
unlock); TLS (`require_auth`); Encrypted legacy client keys and require_auth
are kept.

## Changed by PGMI-914 and PGMI-922 (2026-11-12): the tap signs in with SCRAM

tokio-postgres's SCRAM (postgres-protocol) refuses more than 100,000
iterations, so a role an administrator hashed with 600,000 (the OWASP
figure) could not sign in; Go's cap is 10,000,000. Owner decision
(PGMI-914, option a): no fork, no vendored patch; pgmi's own layer does the
exchange.

Until the first `ReadyForQuery` the tap now reads whole frames. When it
holds a password and the server asks for SASL, it answers itself with
`conn/scram.rs` (SCRAM-SHA-256 and -PLUS, `tls-server-end-point` binding,
SASLprep, iteration cap 10,000,000, server signature checked, the key
derivation on the blocking pool) and keeps those messages from the driver,
which then sees `AuthenticationOk` as after `trust`. `channel_binding=require`
is enforced by the tap in that case (the driver is told `prefer`), with the
driver's own words: `server did not use channel binding`. Cleartext and MD5
stay with the driver. Before the first `ReadyForQuery` a message over 64 KiB
is refused unread (PGMI-922: one header could otherwise make the driver
reserve gigabytes). After it, the tap is the passive reader it was.
