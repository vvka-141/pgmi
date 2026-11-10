# Research: the PostgreSQL driver, and whether pgmi needs a tokio-postgres fork

Date: 2026-11-10. Ticket: PGMI-829. Input to records 0004, 0005 and 0006.
Sources: crates.io API, the downloaded `.crate` sources, GitHub API on
`rust-postgres/rust-postgres` (the repository moved there from
`sfackler/rust-postgres`), the Go code and the black-box cases.

## Verdict

Do not fork. Use upstream tokio-postgres unpatched and put a small **wire tap**
in pgmi's own code. The tap is a stream wrapper (`AsyncRead + AsyncWrite`) that
pgmi passes to `connect_raw`. It reads the PostgreSQL message headers as the bytes go past, and
does two things. It records the status byte of every ReadyForQuery. It refuses an
authentication request that `require_auth` does not allow, before tokio-postgres
ever sees it. That covers both patches with public API, about 150 lines. Offer
`Client::transaction_status()` upstream anyway. This is unproven until a short
spike runs the six cases listed under "Next step".

## 1. What each patch fixes

### Transaction status (`Client::transaction_status()`)

Every ReadyForQuery message carries one byte: `I` (idle), `T` (in a transaction
block), `E` (in a failed block). pgx exposes it as `PgConn().TxStatus()`.
tokio-postgres 0.7.18 reads the message and discards the byte
(`src/simple_query.rs:54`, `src/query.rs:167`). The Go binary reads it in two
places:

| Go call site | What it decides | Cases that fail without it |
|---|---|---|
| `internal/services/deployer.go:294` | after each execution unit: a unit "committed" if the status is `I` (or the head ends in `COMMIT`/`END`, which covers `COMMIT AND CHAIN`). This gives `unitsCommitted`, `executionMode`, and the check "deploy.sql ended inside an open transaction" (exit 13) | `execution/open-transaction`, `execution/atomic-head` (`unitsCommitted: 0`), `execution/tail-autocommit` (`executionMode: psql`) |
| `internal/session/session.go:24` | before the advisory unlock: send `ROLLBACK` only if the status is not `I`. In a failed block every statement, including `pg_advisory_unlock_all()`, fails with 25P02, so the lock would survive until the server noticed the disconnect. A bare `ROLLBACK` when idle raises a WARNING, which would reach the user's stderr | `execution/tail-block-fails` (`stderr_not: WARNING`, second deploy not exit 15), `execution/open-transaction` (second deploy), `exit-codes/15-concurrent-deploy` |

pgmi does not need the status to find the first top-level COMMIT. The splitter
finds that by lexing (`preprocessor.SplitExecutionUnits`, `HeadCommits`). The status
is the server's view of what happened after each unit was sent.

### require_auth

libpq's `require_auth` (PostgreSQL 16+) lists the authentication methods the
client accepts (`password`, `md5`, `gss`, `sspi`, `scram-sha-256`, `oauth`,
`none`, or the same names negated with `!`). The client refuses any other request
before it sends a credential. It also refuses a server that skips authentication when `none` is
not allowed. pgx has it since v5.10 (`pgconn/require_auth.go`, about 200 lines);
Go pgmi only passes the key through. tokio-postgres answers whatever the server
asks (`src/connect_raw.rs:169-211`: cleartext, MD5, SASL).

Case: `tls/require-auth-and-version`, steps 2 and 3. An MD5 user with
`require_auth=scram-sha-256` (in the URI or `PGREQUIREAUTH`) over `sslmode=disable`
must fail with exit 11. Without it, pgmi sends the MD5 hash, logs in and exits 0.
Spike A failed these two steps. In practice it protects against a server or
middlebox that downgrades SCRAM to cleartext or MD5. `channel_binding=require`
covers that only over TLS.

## 2. Upstream

- Maintenance: active, one releasing maintainer (Paolo Barbolini; the original
  author sfackler still comments). tokio-postgres releases: 0.7.13 2025-02-02,
  0.7.14 2025-09-25, 0.7.15 2025-10-08, 0.7.16 2026-01-14, 0.7.17 2026-03-30,
  0.7.18 2026-06-12. The last commit on master was 2026-10-02. There are 202 open
  issues and PRs, 74 of them PRs.
- **Transaction status: no issue or PR exists.** Searches for "transaction
  status", "ReadyForQuery", "transaction_status", "in_transaction", "idle" and
  "PQtransactionStatus" find nothing that asks for it. The nearest is #403 (2018),
  where sfackler says that holding the `Client` by value already shows it is idle.
  That argument is about pipelining, not about server-side BEGIN/COMMIT sent as
  SQL. Record 0005 says the feature "has been asked for upstream for years". That
  claim is not supported and should be removed.
- **require_auth: no issue or PR exists.** Related open PRs add authentication
  methods: OAUTHBEARER (#1378, 2026-08-29) and GSSAPI (#1379, 2026-09-01). Neither
  has a maintainer review yet. #1365 (2026-06) asks to make `md-5` optional.
- **History of connection features:** they rarely land. `verify-ca`/`verify-full`
  was closed unmerged twice (#774 in 2021, #988 in 2023; sfackler: "I don't feel great
  about including options in a library's config that it inherently does not
  support"). `target_session_attrs` modes (#1220) were closed without review in
  2025-06, and #768 (more SSL options) has been open since 2021. Small API additions do get merged:
  #1265 `query_typed_opt` took 5 months, #1145 column type modifier took 18
  months, #1324 `FromSql for Cow<str>` took 2 months.
- **Estimate:** a 25-line `transaction_status()` PR is a small, additive change
  of the kind that gets merged. It might land in 2 to 9 months, in the release after that.
  A `require_auth` PR changes connection config, the area upstream has refused
  for years. Assume it will not land.

## 3. Avoiding the patch in pgmi's own code

### A. Wire tap on the stream (recommended)

`Config::connect_raw<S, T>(stream: S, tls: T)` accepts any
`AsyncRead + AsyncWrite + Unpin` stream. pgmi already owns both ends: the socket it
dials (TCP, Unix socket, Cloud SQL dialer) and the TLS adapter (record 0006),
whose stream type tokio-postgres reads through. The tap wraps the plaintext
side:

- On TLS sessions it sits inside pgmi's TLS stream, so it sees decrypted bytes.
  On plaintext sessions it sits on the socket. If it sees its own write side send
  the 8-byte SSLRequest, it skips the one-byte `S`/`N` answer: on `S` the inner
  tap takes over, on `N` it keeps parsing. With `sslnegotiation=direct` it starts
  out dormant.
- Framing is the protocol's: a 1-byte tag and a 4-byte length, with messages
  split across reads. That is about 60 lines plus a fuzz test. Protocol 3.2
  (PostgreSQL 18) did not change the framing.
- **Status**: on each `Z` message, store the last byte in an atomic. The tap
  stores it in `poll_read` before tokio-postgres decodes the message. The client
  future therefore completes only after the store, so `client.batch_execute(unit).await`
  followed by a load reads the right byte. pgmi never pipelines, so no later
  ReadyForQuery can overtake it.
- **require_auth**: during startup, check each `R` request (0 Ok, 3 cleartext, 5
  MD5, 7 GSS, 9 SSPI, 10 SASL with its mechanism list) against the parsed policy,
  using pgx's `requireAuth` logic as the model. A refused request makes
  `poll_read` return an error and leaves the bytes undelivered. tokio-postgres never sees the request, so it
  sends no credential. pgmi puts the libpq wording
  (`authentication method requirement "scram-sha-256" failed: server requested
  md5 authentication`) in a slot the tap shares, and maps the error to exit 11.
  tokio-postgres already rejects an `AuthenticationOk` in the middle of SASL.
- Failure modes: a framing bug leaves a wrong or stale status. The effect is a wrong
  `unitsCommitted`, or a missed or extra `ROLLBACK` at unlock. The black-box cases
  catch both. A future tokio-postgres 0.8 could change `connect_raw`; the
  tap is ours to adapt. The tap does not depend on any tokio-postgres internals.

### B. Track the state with pgmi's lexer

Each tail unit is one top-level statement, so the lexer knows exactly what was
sent. The server's state then follows from the statement kind and whether it
succeeded:

- `BEGIN`/`START TRANSACTION` → T. `COMMIT`/`END`/`ROLLBACK`/`ABORT`/`PREPARE
  TRANSACTION` → I. A failed `COMMIT` also leaves I. `COMMIT AND CHAIN` and
  `ROLLBACK AND CHAIN` → T. `ROLLBACK TO SAVEPOINT` from E → T.
- Any other error inside a block → E. Outside a block, an error leaves I.
- `CALL` of a procedure that commits, or a `DO` block that commits, works only
  outside a block and ends idle. Inside a block it errors (2D000), so the state
  becomes E. Neither can open a block, and `PREPARE` cannot hold `BEGIN`. These
  cases are all consistent with the rules above.

Hazards: the head is one multi-statement message. If it fails, the state is E or I
depending on whether an explicit `BEGIN` ran before the failing statement, and an
error raised inside a function carries no position. So cleanup must send an
unconditional `ROLLBACK` with notices muted; pgmi drives the notice stream, so muting is
easy. Other hazards are edge rules (`COMMIT AND CHAIN` on an aborted block,
`PREPARE TRANSACTION` failing because `max_prepared_transactions=0`) and
servers that diverge (PGlite, Lakebase). The rules work, but they copy the
server's state machine where the status byte reports it. Keep this as a
cross-check in tests, not as the source of truth.

### C. Probe the server

After each tail unit, send `SELECT now() = statement_timestamp()`. In autocommit
the probe's transaction starts at the probe, so the result is `true` (I). Inside
a block `now()` is when the BEGIN ran, so the result is `false` (T). In a failed
block it raises 25P02 (E). Another probe is `SAVEPOINT p; RELEASE p`, which
raises 25P01 when idle. Costs: one round trip per tail unit; an extra statement in `log_statement=all` and
`pg_stat_statements`; an ERROR in the server log on every failed-block probe;
and the probe itself depends on clock and timing. It works, but it is worse than A.

### D. require_auth without a tap

- Owning the startup with `postgres-protocol` does not work. `connect_raw` always
  sends its own startup message, and there is no way to hand it an authenticated
  stream, so pgmi would have to write the whole client.
- A preflight connection without a password learns the method, but it costs a
  connection and a failed login on the server (log noise, lockout tools). It also
  races against a server that changes between the two connections.

### E. Relax the contract

- Dropping `require_auth` would reverse the owner's decision of 2026-11-10. The
  cost: `PGREQUIREAUTH` and `require_auth=` have to be refused with exit 10
  rather than ignored, and anyone who uses them to block a cleartext or MD5
  downgrade loses that protection. Two steps of `tls/require-auth-and-version`
  change.
- Dropping the status would mean always sending `ROLLBACK` (notice muted) and
  taking `unitsCommitted` and the open-transaction check from the lexer (B). That is
  workable but not needed if A works.

## 4. Other drivers

| Crate (version, date) | Tx status | require_auth | sslmode | Client cert | SCRAM / channel binding | Multi-stmt simple query | Notices while running | Cancel | COPY | Unix socket | Multi-host / target_session_attrs | License, C deps |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| tokio-postgres 0.7.18, 2026-06-12 | no (tap) | no (tap) | disable/prefer/require + direct; pgmi adds the rest | via TLS adapter | yes / yes | `batch_execute` | yes, `Connection::poll_message` | yes, `cancel_query_raw` | yes | yes | yes / any, read-write, read-only | MIT/Apache, none |
| postgres 0.19.14 (sync wrapper) | as above | as above | as above | as above | as above | yes | callback only | yes | yes | yes | as above | same; blocking, no gain |
| sqlx-postgres 0.9.0, 2026-05-21 | internal only (`pub(crate) in_transaction`) | no | all six | yes | yes / **no** (always sends `n,,`) | `raw_sql` | **no**: logged through `tracing` only | **no** | yes | yes | **one host** / no | MIT/Apache, none |
| libpq 6.0.1 (sanpii, 2025-10-29) over pq-sys 0.7.8 (2026-10-30; `bundled` = libpq 18.6 via pq-src 0.3.14) | yes | yes | all six | yes, incl. encrypted keys | yes / yes | yes | yes, notice receiver (C callback) | yes | yes | yes | yes / all six | MIT; **libpq and OpenSSL in C**, built per target |
| pgwire 0.42.2 client, 2026-11-10 | yes | no (pluggable startup handler) | disable/prefer/require | partial | SCRAM, no PLUS | yes | partial | key kept | partial | ? | no | MIT/Apache; its README: client "in progress", "for general purpose ... use rust-postgres" |
| xitca-postgres 0.4.0, 2026-02-20 | no | no | disable/prefer/require | ? | yes | yes | ? | yes | yes | yes | no | Apache-2.0; small user base |
| wtx 0.52.1, 2026-08-14 | no | no | no sslmode | no | yes / yes | yes | no | no | no | no | no | MPL-2.0 |
| zero-postgres 0.9.0, simple_pg_client 0.6.0, pg-proto 0.12.0 | not checked: fewer than 2,000 recent downloads each, one author each | | | | | | | | | | | |

Renamed forks are already common on crates.io: yb-tokio-postgres (Yugabyte,
2026-09-08), encore-tokio-postgres, saola-tokio-postgres, tokio-postgres-fork,
tokio-gaussdb (0.1.0, 2025-06). Each exists to carry a vendor patch. None is a
general alternative, and their low download counts show how little use a fork
gets.

Not drivers: deadpool-postgres 0.14.2, bb8-postgres 0.9.0 and r2d2_postgres are
pools; diesel-async and sea-orm sit on top of tokio-postgres or sqlx and inherit
their gaps; pgrx (formerly pgx) builds server extensions.

**libpq** is the only library that matches the contract out of the box, because it
is the reference implementation. It still loses on cost:

- It brings OpenSSL and a C build to all six targets, which record 0006 bans.
- Every OpenSSL CVE means a pgmi release.
- It cannot use a stream pgmi has already dialled, so the Cloud SQL dialer
  (record 0009) would need a local proxy listener.
- It reads `PG*` from the process environment, against record 0016.
- It does blocking or callback I/O, which needs `AsyncFd` glue that differs on
  Windows.

**sqlx** has more gaps than tokio-postgres: no notice delivery, no cancel, no
channel binding, one host.

## 5. Recommendation

1. Upstream tokio-postgres 0.7.18, unpatched, with workaround A (the wire tap).
   Keep pgmi's own TLS adapter (record 0006), which the tap sits inside.
2. Open one upstream PR for `Client::transaction_status()` (the spike patch,
   with a test). If it is released, delete the status half of the tap. Do not
   spend effort upstreaming `require_auth`.
3. Records to change if the spike holds: 0005 becomes "no fork". 0004's "During
   the run" line says the status comes from the tap. 0006 drops "against the fork's traits".
   0018 drops the extra crate.
4. Fork only if the tap fails the spike. In that case use the plan in record 0005, plus
   a watch on upstream releases and RustSec, and a named owner who rebases within a
   week of a security release.

**Next step (spike, about one day):** build the tap on the spike A branch and
run `execution/open-transaction`, `execution/tail-block-fails`,
`execution/atomic-head`, `execution/tail-autocommit`,
`exit-codes/15-concurrent-deploy` and `tls/require-auth-and-version`. Also run
the TLS and direct-TLS cases, to show the tap does not break negotiation. Add a
fuzz test of the frame parser.

## Residual risks

- The tap is protocol code pgmi owns. It is small and runs only on the read side,
  but a parsing bug would give a wrong status without any error. The parser
  needs fuzzing and the cases above.
- `require_auth` through an I/O error depends on tokio-postgres not buffering
  ahead across the startup boundary. That holds today because it reads only
  through our stream, but it should be pinned by the black-box case.
- Upstream depends on one releasing maintainer. Connection features do not get
  merged there, so pgmi's resolution layer (record 0004) stays permanent either
  way.
- If tokio-postgres goes unmaintained, the fallback is still a fork. The
  tap makes that fork smaller, because it would carry no patches.
