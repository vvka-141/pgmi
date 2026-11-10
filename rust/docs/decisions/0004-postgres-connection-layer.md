# 0004: PostgreSQL driver and connection layer

Status: accepted, 2026-11-10 (owner delegated, see PGMI-829). Ticket: PGMI-829. Builds on spike A (verdict go).
Checked on crates.io, 2026-11-10: tokio-postgres 0.7.18 (2026-06-12,
rust-version 1.85), postgres-protocol 0.6.12.

## Context

Spike A showed that tokio-postgres through `connect_raw`, with pgmi's own
libpq-style resolution on top, passes 25 of 29 connection cases; the other
four were wording or open contract decisions, since settled. The owner's
decided changes make the Rust binary follow libpq in five places.

## Options

1. **tokio-postgres `connect_raw` plus pgmi's own resolution layer** (spike A).
2. **tokio-postgres `Config::connect`.** Accepts three sslmodes, no `PG*`
   variables, no `.pgpass`, no ADO.NET, no per-address errors. Ruled out by
   the spike.
3. **libpq through FFI** (`pq-sys`). The reference behaviour for free, but a
   C library and OpenSSL in every static build, which spike A's 4.6 MB
   static-pie binary avoids. Cancel and notices would go through C callbacks.
4. **sqlx.** A pool-oriented driver with the same gaps as option 2.

## Decision

Option 1, on unpatched tokio-postgres with the connection layer of record 0005. pgmi's code owns everything
above the wire protocol:

- **Parsing**: URI (several hosts, bracketed IPv6), keyword/value (quoting as
  libpq), ADO.NET (Npgsql names, `Host=h:port`). One `ConnSpec` with a
  provenance tag on every field, which feeds `connection: from ...`,
  `overrides` and `connectionSource` (record 0019).
- **Layering**: flags > `PGMI_CONNECTION_STRING` > `DATABASE_URL` > `PG*` >
  `pgmi.yaml` > defaults, with the refusals in "Connection sources", read
  from the environment snapshot (record 0016), never from `std::env`.
- **Unknown keys** are refused before connecting, exit 10,
  `invalid connection option "<key>"` (decided change). `PGSERVICE` and
  `service=` are refused, `hostaddr` is refused as today, `PGHOSTADDR` and
  `PGSSLCRL` are ignored. The three pgx keys are accepted:
  pgmi only uses the simple protocol and unnamed statements
  (`batch_execute`, `query_typed`), so PGlite works without them.
- **Ports**: `PGPORT` and `port=` may list one port per host, or one for all
  (decided change); any other count is exit 10.
- **Passwords**: string, then `PGPASSWORD`, then `.pgpass`/`PGPASSFILE`
  (wildcards, escapes, the 0600 check with a WARNING; no check on Windows,
  `%APPDATA%\postgresql\pgpass.conf`).
- **Hosts and addresses**: each host in order; each host name resolved with
  `tokio::net::lookup_host` and every address tried; the final error lists
  every host and address with its own error (intended change). Unix socket
  directories through `tokio::net::UnixStream` (Unix only).
- **Timeouts**: `connect_timeout`/`PGCONNECT_TIMEOUT` per attempt; `--timeout`
  bounds the whole connect phase and is exit 11 there.
- **Retries**: SQLSTATE class 08, 53300, 57P03, refused, reset, unreachable
  and timeout are retried for 4 attempts in all, after about 0.1, 0.2 and
  0.4 s, each logged on stderr; nothing else is retried.
- **sslmode=allow**: plaintext first; if the server refuses the plaintext
  startup, try again with TLS, as libpq does (decided change).
- **target_session_attrs**: all six values, one probe query per host
  (`SHOW transaction_read_only`, `pg_is_in_recovery()`), a second pass for
  `prefer-standby`.
- **Startup**: `application_name` defaults to `pgmi` (`PGAPPNAME` wins);
  `options`/`PGOPTIONS` and `PGTZ` reach the server.
- **During the run**: pgmi drives the `Connection` future itself, so notices
  arrive while a statement runs; transaction status comes from the layer of
  record 0005; cancel uses `cancel_query_raw` over the
  same TLS setup, then waits up to 2 s for the server to report it.
- **Loading** the project into the temp tables uses parameterised
  `INSERT ... SELECT * FROM unnest($1::text[], ...)` in batches, all with
  unnamed statements, so the session API SQL in `shared/` stays the only
  definition of the tables.

## Consequences

- About 2,000 lines of resolution and dialing code that pgmi owns and the
  black-box suite pins (41 driver rows in `blackbox/COVERAGE.md`).
- **No password:** as libpq, pgmi fails only when the server asks for a
  password and none was found: `no password supplied`, naming the places
  looked at, exit 11 (contract reworded 2026-11-10).
- `default_query_exec_mode` is one of the three PGlite keys; its value is
  accepted and ignored, since pgmi has one execution mode.
- Go speaks protocol 3.2 when `max_protocol_version=3.2` is set, including
  the longer 3.2 cancel key (`connection/protocol-version`, PGMI-830).
  tokio-postgres always sends 3.0. The case passes as long as the keys are
  accepted, but `min_protocol_version=3.2` then means a 3.0 session the
  user said to refuse: the layer either refuses it (exit 10, naming the
  key) or implements 3.2 itself. Owner decision, listed in `go-review.md`.

## Serves

Connection sources; Connection resolution; Execution contract; Concurrency;
Target database, overwrite and approval; Session API; Notices and the summary
line; Timeout, cancellation, interruption; Every address's error is reported;
sslmode=allow retries with TLS; Unknown connection-string keys are refused
before connecting; PGPORT may list several ports; No password available.
