# 0.20 release notes: material collected before switch day

Switch day (PGMI-835) moves this into `RELEASES.md` and the README, then
deletes this file. Until then the README describes the Go binary, so the
Rust-only requirements live here.

## Requirements (README "Requirements" line)

- **CPU.** pgmi uses the graviola crypto library for TLS and cloud sign-in.
  It needs, on x86-64: AES-NI, PCLMULQDQ, SSSE3, AVX, AVX2, BMI1, BMI2 and
  ADX (Intel Broadwell, AMD Zen and later, about 2014 on); on arm64: NEON,
  AES, PMULL and SHA2 (Apple silicon, AWS Graviton, Raspberry Pi 5; not
  Raspberry Pi 4). On a CPU without them, a connection that uses TLS and
  any cloud sign-in stop with exit 10 and name the missing features;
  commands that need no encryption run as before.

## Behaviour changes

The full list, with old and new exit codes, is the section "Behaviour
changes in 0.20" of `docs/design/behaviour-contract.md`.

## Lines for the notes (decided changes, one line each)

- **Templates on managed clouds.** `pgmi init` no longer writes `sslmode:
  prefer`, so a new project works with `--azure`, `--aws`, `--google` and
  `--databricks` as it is. An older project that still has the line gets an
  error naming the line and the fix. The basic template's privilege test
  checks only the deploying role's functions, so it passes on Lakebase.
- **Secrets stay out of the output.** The connection password and every
  sign-in token are shown as `***` wherever pgmi prints them: notices,
  errors, `--json` and `pgmi serve` answers.
- **`pgmi serve` limits.** A request line may hold at most 16 MiB, at most 32
  tool calls run at once, and a client that leaves 1024 answers unread is
  disconnected. A file with two `<pgmi-meta>` elements in one comment is
  refused (exit 10).
- **Closer to PostgreSQL.** `ÄCALL pgmi_test()` is one identifier, as
  PostgreSQL reads it, so pgmi no longer expands it. `--overwrite -d POSTGRES`
  compares database names exactly, as PostgreSQL does. The wizards' lists
  wrap around at both ends.
- **The advanced template on managed hosts.** A first deploy names a login's
  missing `CREATEDB` or `CREATEROLE`, and the extensions Azure has not
  allow-listed, before it changes anything. On Databricks Lakebase it sets
  `provider_role_pattern` and `provider_schemas` itself. Both template
  READMEs say what each host needs.
- **The Go library is gone.** pgmi 0.20 is a Rust program with no library
  crate. Go programs that embedded `pkg/pgmi` pin the `go-legacy` tag; Python
  and Node get thin wrappers over `deploy --json` (PGMI-850).
- **Google Cloud SQL** goes through pgmi's own dialer, and a transient
  connection failure is retried like on every other route.
- **Whole-deployment retry inside `--timeout`.** A transient failure (a lost
  connection, a server restart, too many connections, a deadlock or
  serialization failure, a lock timeout, a failover to a standby, a sign-in
  endpoint that stays down) runs the deploy again from the start, exactly
  like a redeploy, waiting 1 s doubling to 30 s, until it succeeds or the
  time box ends. A failing test or any other SQL error is never retried.
  `--json` lists the `attempts`; `retry: false` in `pgmi.yaml` turns it off.
  Pipelines need no retry step of their own. See
  [Time box and retries](switch-day/timeout-and-retry.md).
