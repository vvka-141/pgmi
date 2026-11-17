# Spike A: the PostgreSQL connection layer in Rust

Date: 2026-11-10. Ticket: PGMI-846. Verdict: **go**.

## Question

pgx (Go) adds connection behaviour that pgmi's own code never shows: the
remaining `PG*` variables, `~/.pgpass`, the `~/.postgresql/` defaults, sslmode
fallback, every `target_session_attrs` value, multi-host with joined errors,
unknown connection-string keys sent as server settings. tokio-postgres does
none of it, accepts three sslmode values, and hides the transaction status pgmi
reads after every deploy unit. Can a Rust connection layer match pgx, judged by
the black-box suite rather than on paper?

## What was built

A throwaway binary (about 600 lines, branch `feature/pgmi-846-connection-spike`,
archived) that implements just enough of `pgmi deploy` for the connection cases:

- tokio-postgres 0.7.18 through `connect_raw`, with tokio-postgres-rustls 0.14
  and rustls 0.23 on the ring backend. tokio-postgres keeps the protocol work:
  SSLRequest, direct negotiation, SCRAM and channel binding, cancel.
- pgmi's own resolution layer on top: `PG*` variables, URI and keyword/value
  strings, flags, `~/.pgpass` with the permission check, `~/.postgresql/`
  defaults, multi-host with all six `target_session_attrs` values decided by a
  probe query per host, errors joined across hosts.
- One rustls verifier for every sslmode: no verification for `require`,
  chain-only for `verify-ca` (a name mismatch is accepted), full for
  `verify-full`; `require` with a root certificate becomes `verify-ca` as in
  pgx; `sslrootcert=system` reads the platform store (and `SSL_CERT_FILE`).
- A 25-line patch to tokio-postgres
  ([patches/fork-transaction-status-0.7.18.patch](patches/fork-transaction-status-0.7.18.patch)): the
  codec records the status byte of every ReadyForQuery, including the one after
  an error, and `Client::transaction_status()` returns it.
- Notices streamed from the connection driver while a statement runs; cancel on
  `--timeout` and SIGINT through `cancel_query_raw` over TLS; `--timeout`
  bounding the connect phase as exit 11.

## Result against the black-box suite

29 connection cases (`tls/`, `connection/`, `cancel/`, `notices/` and the
connection rows of `env/` and `execution/`), run with `PGMI_BIN` pointing at the
spike: **25 pass**, on the glibc build and on the static musl build alike.

| Failing case | Cause | Cost |
|---|---|---|
| `connection/pgpass-world-readable` | exit code and warning right; the error text differs (tokio-postgres refuses to connect without a password, pgx sends none and reports the server's refusal) | wording; decide in the contract |
| `connection/target-session-attrs` | the spike accepts `PGPORT=5432,5433` as libpq does; Go refuses it (exit 10) | a contract decision, not a gap |
| `tls/encrypted-client-key` | legacy OpenSSL-encrypted PEM keys (`Proc-Type: 4,ENCRYPTED`) not implemented; no crate decrypts them | about 50 lines (EVP_BytesToKey with MD5, AES-CBC), or declare out in the contract |
| `tls/require-auth-and-version` (2 steps) | tokio-postgres has no `require_auth` | a second small patch: record the authentication request type during startup and refuse a method not listed |

## The 41 research items

✓ passed in the spike, ✗ failed, — not tried (outside a connection spike).

| # | Item | Spike |
|---|---|---|
| 1–3 | execution modes (atomic head, psql tail) | — pgmi's splitter, not the driver; `batch_execute` runs a multi-statement simple query |
| 4 | open transaction at the end detected and rolled back | ✓ `execution/open-transaction` (needs the patch) |
| 5–8 | tail block failure, 25P01, loader, PGlite | — |
| 9 | NOTICE streamed while running | ✓ `notices/streaming` |
| 10, 11 | severities, DETAIL, HINT, notice before error | ✓ `notices/severity` |
| 12–15 | error attribution | — pgmi logic; tokio-postgres exposes position, where, internal query |
| 16 | connection error messages | partly: exit codes ✓, wording is a formatting layer |
| 17, 18 | `--timeout` cancels on the server, over TLS and direct TLS | ✓ `cancel/timeout-cancels`, `cancel/timeout-over-tls` |
| 19 | cancel blocked by a firewall | — |
| 20 | SIGINT twice | ✓ `cancel/sigint-twice` |
| 21 | connect timeout = 11 | ✓ `env/pgconnect-timeout` |
| 22 | URI and keyword/value | ✓; ADO.NET — |
| 23 | `PG*` variables, `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGCHANNELBINDING` | ✓; `PGREQUIREAUTH` ✗ |
| 24 | `.pgpass`, wildcard, `PGPASSFILE`, permissions | ✓ (world-readable: wording only) |
| 25 | `PGSERVICE` refused | implemented, not run |
| 26 | multi-host failover, joined error | ✓ `connection/multi-host` |
| 27 | `target_session_attrs`, all six values | ✓ (the `PGPORT` list step differs by decision) |
| 28 | `options`, unknown keys sent to the server | ✓ `connection/keywords` |
| 29 | Unix socket | ✓ `connection/unix-socket` |
| 30, 31 | sslmode matrix, `~/.postgresql/root.crt`, require + root = verify-ca, name mismatch | ✓ `tls/sslmode-matrix`, `tls/sslmode-refusals`, `tls/default-files*` |
| 32 | `sslrootcert=system`, `SSL_CERT_FILE` | ✓ `tls/system-roots` |
| 33 | server without TLS, TLS-only server | ✓ `tls/tls-only-and-plain-servers` |
| 34 | `sslnegotiation=direct` | ✓ (PostgreSQL 16 refusal —) |
| 35 | client certificate by flags, `PGSSL*`, default files | ✓ `tls/client-certificate`; encrypted key ✗ |
| 36 | SCRAM channel binding over TLS, refused without | ✓ `tls/channel-binding-and-direct` |
| 37 | `require_auth` | ✗ |
| 38 | TLS version in `--json` | ✓ |
| 39, 40 | token callbacks, Cloud SQL dialer | — spike B (PGMI-847) |
| 41 | static musl build | ✓ `static-pie`, 4.6 MB, no OpenSSL, same 25 of 29 |

## Decision

Go. Build pgmi's connection layer on tokio-postgres `connect_raw` with rustls,
keeping the libpq-style resolution in pgmi's own code, as the spike does.

Consequences for the design (PGMI-829):

- Vendor tokio-postgres with two patches, transaction status (done here) and
  `require_auth`, and offer both upstream. Pin the vendored version.
  (Superseded by record 0005: no fork, no upstream PR.)
- Error text is pgmi's own layer: map rustls and tokio-postgres errors to the
  wording the contract fixes, keeping the full cause chain.
- Encrypted legacy keys: hand-write the decryption or drop the feature in the
  contract; nothing else needs code outside the crates used here.
- The contract decides the three places where pgx and libpq differ and the spike
  followed libpq or did not try: `PGPORT` lists, `sslmode=allow` (the spike, like
  pgx, never retries with TLS), and the password-missing message.
