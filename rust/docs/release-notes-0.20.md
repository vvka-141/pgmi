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
- **`pgmi serve` guards the operator's credentials and databases.** The
  operator's password, password file and cloud sign-in go only to its own
  server and those in `PGMI_SERVE_HOSTS`, and a tool call may drop a
  database (`overwrite`) only when `pgmi serve` runs with
  `PGMI_SERVE_ALLOW_OVERWRITE=1`. See
  [serve-allowed-hosts](switch-day/serve-allowed-hosts.md).
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
- **The advanced template's deploy.sql reads top to bottom.** Five banners
  (PREFLIGHT, BOOTSTRAP, DEPLOY, VERIFY, TAIL) and the file loop in plain
  sight; the setup lives in `deploy/` files it runs by name, with the lists a
  project edits in `deploy/settings.sql` and hosting providers in
  `deploy/providers.sql`. Parameters are declared in `deploy/parameters.xml`
  (was `session.xml`, root element `<parameters>`) and read only as
  `pgmi.<key>` (the `deployment.<key>` copies are gone). On a server older
  than PostgreSQL 18 the refusal says the project's code can be adapted.
  **Upgrading:** move `session.xml` to `deploy/parameters.xml` and rename its
  root element, and replace `current_setting('deployment.<key>')` with
  `current_setting('pgmi.<key>')` in your files, except in run-once files that
  already ran: an edit there is refused as drift. A project whose
  `lib/internal/environment.sql` already ran keeps that file, which reads
  `deployment.env` on a fresh database, so it also keeps one line in
  `deploy/parameters.sql`, beside the `pgmi.` `set_config`:
  `PERFORM set_config('deployment.env', v_param.value, false) WHERE v_param.key = 'env';`
- **The Go library is gone.** pgmi 0.20 is a Rust program with no library
  crate. Go programs that embedded `pkg/pgmi` pin the `go-legacy` tag; Python
  and Node get thin wrappers over `deploy --json` (PGMI-850).
- **Google Cloud SQL** goes through pgmi's own dialer, and a transient
  connection failure is retried like on every other route.
- **`sslmode=verify-ca` without a root file checks the host name.** When no
  `sslrootcert` is given, the system trust store vouches for the server, so
  pgmi also checks the server's name, as `verify-full` does. With an
  explicit root file `verify-ca` is unchanged.
- **A project `.env` cannot redirect credentials.** Keys that locate
  credentials, endpoints, proxies or trust roots (`AWS_CONFIG_FILE`,
  `SSL_CERT_FILE`, `HTTPS_PROXY` and the rest listed in the behaviour
  contract) are refused when a checked-out project sets them; the
  `.env applied:` line names them. Set them in the environment pgmi runs in.
- **`pg_temp.pgmi_run_file(path)` in every deploy session.** deploy.sql can
  run one project file by its path, with errors named by file and line as
  before; both templates use it, and a helper of your own keeps working. See
  [Session API](../../docs/session-api.md#running-one-file-pg_temppgmi_run_file)
  and `pgmi ai contract`.
- **Whole-deployment retry inside `--timeout`.** A transient failure (a lost
  connection, a server restart, too many connections, a deadlock or
  serialization failure, a lock timeout, a failover to a standby, a sign-in
  endpoint that stays down) runs the deploy again from the start, exactly
  like a redeploy, waiting 1 s doubling to 30 s, until it succeeds or the
  time box ends. A failing test or any other SQL error is never retried.
  Once a unit has committed, a failure is final as in Go unless
  `retry_after_commit: true` in `pgmi.yaml` allows a rerun. A connection
  lost while a statement of deploy.sql runs is never retried: whether it
  committed is unknown, so pgmi says so and exits 13. Nor is a failed
  deploy.sql that is one `CALL`, or one `DO` block that says `COMMIT`: it
  may have committed inside itself.
  `--json` lists the `attempts`; `retry: false` in `pgmi.yaml` turns it off.
  Pipelines need no retry step of their own. See
  [Time box and retries](switch-day/timeout-and-retry.md).
- **Security fix: functions planted in a schema on the search_path.** pgmi
  sets up its session objects under `search_path = pg_catalog, pg_temp`,
  calls every built-in by its `pg_catalog` name, and gives each helper
  function its own `search_path`; the deploy's own `search_path` is restored
  before deploy.sql runs. Before, a role that could create functions in a
  schema on the deployer's `search_path` (`public` up to PostgreSQL 14 and on
  clusters upgraded from it) could have pgmi run its function with the
  deployer's rights during session set-up. **pgmi 0.14 (Go) is affected too.**
