# Where Rust is better than Go

The evidence for the ship decision (PGMI-835) and the 0.20 release story.
Owner principle, 2026-11-11: parity is the floor; every difference must make
Rust better. Each line names its proof.

## Measured, 2026-11-11

Go: the 0.14 code built as the release builds it (`CGO_ENABLED=0`,
`-trimpath -ldflags "-s -w"`, same size as the shipped 0.14.1 binary). Rust:
`cargo build --release` at 31d40ff9. Linux x86-64, 16 cores, PostgreSQL 18 in
Docker on the same machine. Go and Rust ran alternately in every round, on a
shared machine (load about 6), so absolute numbers are noisy and the ratios
are what counts. Medians; script `bench.py` (scratchpad, not kept).

| Measure | Go 0.14 | Rust 0.20 | Rust vs Go |
|---|---|---|---|
| Binary size (static, x86-64) | 38.7 MB | 21.6 MB (glibc), 21.8 MB (musl) | 44% smaller |
| Start-up, `pgmi --version` (100 runs) | 8.6 ms | 2.3 ms | 3.7× faster |
| `metadata plan` of the advanced template (30 runs) | 45.7 ms | 7.7 ms | 5.9× faster |
| Deploy the basic template into a new database (9 runs) | 63.3 ms | 48.6 ms | 23% faster |
| Redeploy the basic template (9 runs) | 56.8 ms | 43.0 ms | 24% faster |
| Deploy the advanced template into a new database (5 runs) | 3.45 s | 3.38 s | same: the server's work dominates |
| Peak memory, `--version` | 26.7 MiB | 6.4 MiB | 4× less |
| Peak memory, basic deploy | 31.7 MiB | 10.8 MiB | 3× less |
| Peak memory, advanced deploy | 43.3 MiB | 17.9 MiB | 2.4× less |

The measuring found one Rust defect first: Rust did not analyse the session
tables after loading, as Go does, and lost about 200 ms per deploy to JIT
compilation of pgmi's own test-plan query. Fixed in 31d40ff9 with the case
`deploy/session-tables-analyzed`; the table shows the numbers after the fix.

## Behaviour (decided changes with tests)

Each is a row in "Behaviour changes in 0.20" of
`docs/design/behaviour-contract.md`, with its case or test.

- **Secrets stay out of the output.** The password, `sslpassword` and every
  sign-in token are `***` in notices, errors, `--json` and serve answers
  (`notices/secrets-redacted`). Go prints them.
- **Connection-string errors never echo the string**, so a password in it
  never reaches a log.
- **No connection setting is silently ignored.** Every key is honoured or
  refused before connecting (PGMI-860, a rule test). Go passes several to the
  server, which fails later with exit 11.
- **Safer TLS.** `sslrootcert=system` refuses a weaker sslmode; a client key
  other users can read is refused, as libpq does.
- **More of libpq.** `sslmode=allow` retries with TLS; `PGPORT` lists;
  `hostaddr`; `PGCLIENTENCODING`; `keepalives*`, `tcp_user_timeout` and
  `load_balance_hosts` honoured; encrypted PKCS#8 client keys open (Go
  cannot read them).
- **Errors before connecting, not after.** A file name that is not UTF-8, an
  unreadable `.pgmiignore`, unknown connection keys: exit 10 before the target
  database is created.
- **Every address's error is reported** when a host has several.
- **Clearer cloud errors.** A refused token names the database role and
  `--username`; a `pgmi.yaml` sslmode that token sign-in refuses names its
  line and the fix.
- **Native Databricks Lakebase sign-in** with credential refresh (new).
- **Azure certificate sign-in** works; in Go it was unreachable.
- **`pgmi serve` is bounded and exact**: line, call and queue limits; request
  ids matched by value; a repeated id refused.
- **Closer to PostgreSQL's own rules**: identifiers lexed as PostgreSQL does
  (`ÄCALL`), database names compared exactly.
- **Pure Rust, no C** anywhere in the build (`rust/check-no-c.sh`), static
  binaries for every target.

## Engineering

- One SQL lexer shared by every feature; the environment read once (`Env`);
  one place maps errors to exit codes (record 0022).
- Fuzz and property targets for the protocol readers, the connection-string
  parser and the core parsers, in a blocking CI job (PGMI-861, 864, 870).
- Wizards testable without a terminal; serve testable in-process.
- Clippy pedantic, `cargo deny`, MSRV checked in CI.
