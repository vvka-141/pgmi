# Coverage

Every user-visible behaviour of pgmi, and the case that checks it from outside.
The behaviour contract (PGMI-828) maps each row to a section.

- ✓ `case` — covered by that case under `cases/`
- → PGMI-nnn — planned in that ticket
- ✗ reason — not checkable from outside; the reason names what covers it instead

## Exit codes

| Code | Meaning | Coverage |
|---|---|---|
| 0 | success | ✓ `exit-codes/0-success` |
| 1 | general error | ✓ `commands/init-unwritable` (the only trigger found: a filesystem failure) |
| 2 | usage error | ✓ `exit-codes/2-usage` |
| 3 | internal panic | ✗ a release binary has no way to panic on purpose; the Go unit tests (`pgmi_testhooks`) cover the mapping |
| 10 | invalid configuration | ✓ `exit-codes/10-invalid-config` (empty `PGMI_PARAM_*`), `flags/params-file`, `flags/granular-flags`, `flags/compat`, `env/connection-string-vars-refuse-flags`, `env/pgservice-refused`, `config/pgmi-yaml-invalid`, `commands/metadata-invalid`, `commands/metadata-duplicate`, `commands/info` |
| 11 | connection failed | ✓ `exit-codes/11-connection-failed`; `--timeout` and `PGCONNECT_TIMEOUT` while connecting ✓ `env/pgconnect-timeout` |
| 12 | overwrite denied | ✓ `exit-codes/12-overwrite-denied` |
| 13 | SQL failed | ✓ `exit-codes/13-sql-failed` |
| 14 | deploy.sql missing | ✓ `exit-codes/14-no-deploy-sql` |
| 15 | concurrent deploy | ✓ `exit-codes/15-concurrent-deploy` |
| 16 | `--timeout` exceeded | ✓ `exit-codes/16-timeout` |
| 130 | interrupted | ✓ `exit-codes/130-interrupted` (SIGINT), `exit-codes/130-sigterm` |

## Commands

| Command | Coverage |
|---|---|
| `deploy` | ✓ `exit-codes/*`, `deploy/*`, `params/*`; flags below |
| `info`, `info --json`, missing path = 10 | ✓ `commands/info` |
| `init` (no `-t` without a terminal = minimal; `-t basic\|advanced`; manifests written without `.scaffold`; non-empty target = 2; unknown template = 2; unwritable target = 1) | ✓ `commands/init`, `commands/init-unwritable` |
| `init` wizard (terminal, no `-t`) | ✓ `terminal/init-wizard` |
| `config` (wizard: tests the connection, keeps pgmi.yaml comments and params, saves to `~/.pgpass`) | ✓ `terminal/config-wizard` |
| `metadata plan` (+`--json`) | ✓ `commands/metadata`, `commands/metadata-invalid` |
| `metadata validate` (+`--json`), bad id = 10, duplicate id = 10 | ✓ `commands/metadata`, `commands/metadata-invalid`, `commands/metadata-duplicate` |
| `metadata scaffold` (preview, `--write`, `--sort-key`, `--idempotent=false`) | ✓ `commands/metadata`, `commands/metadata-scaffold-run-once` |
| `templates list`, `templates describe <t>`, unknown = 2 | ✓ `commands/templates` |
| `version`, `--version`, extra argument = 2 | ✓ `commands/version` |
| `ai`, `ai contract`, unknown subcommand = 2 | ✓ `commands/ai` |
| `completion bash\|zsh\|fish\|powershell`, other shell = 2 | ✓ `commands/completion` |
| `help`, `--help`, `<command> --help`, unknown command / wrong arity = 2 | ✓ `commands/help` |
| `-v` / `--verbose` (DEBUG notices shown) | ✓ `commands/verbose` |
| `serve`: initialize, `tools/list` (7 tools), a tool call, malformed message (-32700, session continues), unknown method (-32601), EOF exits 0 | ✓ `serve/session` |
| `serve`: `deploy` tool with params, ping answered during a deploy, `notifications/cancelled` rolls the deploy back | ✓ `serve/deploy-tool`, `serve/cancel` |

## Deploy flags

| Flag | Coverage |
|---|---|
| `--connection` URI | ✓ every `deploy` case |
| `--connection` keyword/value, ADO.NET | ✓ `flags/connection-forms` |
| `-h`, `-p`, `-U`, `-d`; `-h` with `--connection` = 10 | ✓ `flags/granular-flags` |
| `--compat` | ✓ `flags/compat` |
| `--force` (alone; with `--overwrite`, no countdown without a terminal) | ✓ `flags/overwrite-force` |
| `--overwrite` without `--force`, no terminal | ✓ `exit-codes/12-overwrite-denied` |
| `--json` | ✓ `deploy/json-success`, `deploy/json-failure` |
| `--param` | ✓ `params/precedence` |
| `--params-file` (file, `-` stdin, later file wins, case clash = 10, `-` twice = 10, missing file = 10); `--param` without `=` = 10 | ✓ `params/precedence`, `flags/params-file` |
| `--timeout` (`3s`, `90s`, `1h30m`; not a duration = 2) | ✓ `exit-codes/16-timeout`, `flags/timeout-forms` |
| `--sslmode`, `--sslcert`, `--sslkey`, `--sslrootcert`; `--sslmode` overrides `DATABASE_URL`'s, refused next to `--connection` (10) | ✓ `tls/client-certificate`, `tls/sslmode-flag-overrides` |
| `--azure`, `--azure-tenant-id`, `--azure-client-id` | → PGMI-853 |
| `--aws`, `--aws-region` | → PGMI-853 |
| `--google`, `--google-instance` | → PGMI-853 |

## Environment

| Variable | Coverage |
|---|---|
| `PGMI_PARAM_<NAME>` | ✓ `params/env-var-reaches-session`, `params/precedence`, `exit-codes/10-invalid-config` |
| `PGMI_CONNECTION_STRING`, `DATABASE_URL` (and their order, and the `connection: from ...` line) | ✓ `env/connection-string-vars` |
| `-h`/`-p`/`-U` next to `PGMI_CONNECTION_STRING` or `DATABASE_URL` = 10 | ✓ `env/connection-string-vars-refuse-flags` |
| `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGAPPNAME`, `PGCONNECT_TIMEOUT` | ✓ `env/pg-variables`, `env/pgappname`, `env/pgconnect-timeout` |
| `PGPASSFILE`, `~/.pgpass` | ✓ `connection/pgpass*` |
| `PGSERVICE` and `service=` refused | ✓ `env/pgservice-refused` |
| `PGSSLMODE`, `PGSSLCERT`, `PGSSLKEY`, `PGSSLROOTCERT`, `PGSSLPASSWORD` | ✓ `tls/client-certificate`, `tls/encrypted-client-key` |
| `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGREQUIREAUTH`, `PGCHANNELBINDING` (read by the driver) | ✓ `connection/keywords`, `connection/target-session-attrs`, `tls/require-auth-and-version`, `tls/channel-binding-and-direct` |
| `PGHOSTADDR`, `PGSSLCRL` (ignored) | ✓ `env/ignored-variables` |
| `NO_COLOR`, `PGMI_NO_BANNER`, `CI`, `PGMI_NON_INTERACTIVE` (on a terminal) | ✓ `terminal/colors`, `terminal/force-banner`, `terminal/non-interactive` |
| `PGMI_PAGER`, `PAGER`, `PGMI_PAGER=cat` (on a terminal) | ✓ `terminal/pager` |
| `AZURE_*`, `AWS_REGION`, `AWS_DEFAULT_REGION` | → PGMI-853 |

## Project configuration

| Source | Coverage |
|---|---|
| `pgmi.yaml` connection, params, timeout; unknown key = 10 | ✓ `config/pgmi-yaml`, `config/pgmi-yaml-invalid` |
| precedence: flags > environment > `pgmi.yaml` > defaults, with the `connection: from ...` line | ✓ `config/precedence`, `config/pgmi-yaml`, `env/connection-string-vars` |
| project `.env` loaded first, set variables win, `dotenvKeys` in `--json` lists only the keys it set | ✓ `config/dotenv` |
| `.pgmiignore` (globs, classes, `**`, directories, `!`, escaped `#`; hiding deploy.sql = 10) | ✓ `dialect/pgmiignore`, `dialect/pgmiignore-hides-deploy`, `dialect/pgmiignore-posix-class` |

## Connection layer

Numbers are the parity cases in the driver research (`research-pg-driver.md`,
"Black-box parity tests").

| # | Behaviour | Coverage |
|---|---|---|
| 1 | no top-level terminator: one transaction, nothing committed on failure, `executionMode=atomic` | ✓ `execution/atomic-head` |
| 2 | `CREATE INDEX CONCURRENTLY` in the tail | ✓ `execution/tail-autocommit` |
| 3 | tail failure keeps earlier autocommitted statements, `executionMode=psql` | ✓ `execution/tail-autocommit` |
| 4 | ending inside an open transaction fails, rolls back, releases the lock | ✓ `execution/open-transaction` |
| 5 | failure inside an explicit tail `BEGIN` rolls back, releases the lock, no stray WARNING | ✓ `execution/tail-block-fails` |
| 6 | `CALL pgmi_test()` without `BEGIN` = 25P01; inside a block a failing test fails the deploy | ✓ `execution/pgmi-test-needs-begin`, `execution/pgmi-test-in-block` |
| 7 | loader at scale, unicode, missing description, plan order | ✓ `execution/loader-at-scale` (3000 files) |
| 8 | PGlite single-session server | ✗ needs a PGlite server; the Go integration test against PGlite and the manual check in `docs/CONNECTIONS.md` cover it |
| 9 | NOTICE streamed while the statement runs | ✓ `notices/streaming` |
| 10 | severities, DETAIL, HINT, LOG hidden | ✓ `notices/severity`; localized server messages ✗ (no non-English locale in the image) |
| 11 | a notice before an error prints before it | ✓ `notices/severity` |
| 12 | syntax error in deploy.sql | ✓ `errors/head-syntax` |
| 13, 14 | error inside an executed file names the file | ✓ `errors/attribution` |
| 15 | `--json` failure record: `sqlstate`, `failedFile` | ✓ `errors/attribution` |
| 16 | 42501 TEMPORARY guidance; wrong password; missing maintenance database; hba reject | ✓ `errors/temporary-privilege`, `errors/connection-failures`, `tls/tls-only-and-plain-servers`; 53300, 57P03 ✗ (server states a case cannot produce reliably) |
| 17 | `--timeout` cancels on the server, no backend left | ✓ `cancel/timeout-cancels` |
| 18 | cancel over TLS and over direct TLS negotiation | ✓ `cancel/timeout-over-tls` |
| 19 | cancel request blocked by a firewall | ✗ needs a firewall between client and server; `internal/db/cancel_integration_test.go` covers it |
| 20 | SIGINT twice = 130 at once | ✓ `cancel/sigint-twice` |
| 21 | unreachable host with a connect timeout = 11, not 16 | ✓ `env/pgconnect-timeout` |
| 22 | URI, keyword/value and ADO.NET forms | ✓ `flags/connection-forms` |
| 23 | `PG*` variables, `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGCHANNELBINDING`, `PGREQUIREAUTH` | ✓ `env/pg-variables`, `connection/keywords`, `connection/target-session-attrs`, `tls/channel-binding-and-direct`, `tls/require-auth-and-version` |
| 24 | `.pgpass`, wildcard, `PGPASSFILE`, wrong entry, world-readable file ignored | ✓ `connection/pgpass`, `connection/pgpass-wildcard-and-file`, `connection/pgpass-world-readable` |
| 25 | `service=` / `PGSERVICE` refused | ✓ `env/pgservice-refused` |
| 26 | multi-host: a down first host is skipped; both down names both | ✓ `connection/multi-host` |
| 27 | `target_session_attrs`, all six values, against a primary and a standby | ✓ `connection/target-session-attrs` |
| 28 | `options=` applied; unknown key sent to the server and refused (exit 11) | ✓ `connection/keywords` |
| 29 | Unix socket directory, with and without a port | ✓ `connection/unix-socket` |
| 30 | every sslmode; `~/.postgresql/root.crt`; `require` + root = verify-ca | ✓ `tls/sslmode-matrix`, `tls/sslmode-refusals`, `tls/default-files`, `tls/default-files-good-root` |
| 31 | hostname mismatch: verify-ca passes, verify-full fails | ✓ `tls/sslmode-matrix`, `tls/sslmode-refusals` |
| 32 | `sslrootcert=system`, with `SSL_CERT_FILE` | ✓ `tls/system-roots` |
| 33 | server without TLS: prefer plaintext, require refused; TLS-only server: allow refused | ✓ `tls/tls-only-and-plain-servers` |
| 34 | `sslnegotiation=direct` | ✓ `tls/channel-binding-and-direct`; failure against PostgreSQL 16 ✗ (one server version per run) |
| 35 | client certificate by flags, by `PGSSL*`, by `~/.postgresql/postgresql.crt`; encrypted key with `sslpassword`/`PGSSLPASSWORD`; wrong password = 10 | ✓ `tls/client-certificate`, `tls/default-files-good-root`, `tls/encrypted-client-key` |
| 36 | channel binding with an RSA certificate; refused without TLS | ✓ `tls/channel-binding-and-direct`; ECDSA and Ed25519 certificates ✗ (one server certificate per run) |
| 37 | `require_auth` refuses another method | ✓ `tls/require-auth-and-version` |
| 38 | `--json` TLS version | ✓ `tls/require-auth-and-version` |
| 39, 40 | token callbacks per dial, Cloud SQL dialer | ✗ in-process hooks; the cloud cases (PGMI-853) cover them from outside |
| 41 | static musl build | ✗ a property of the Rust build; PGMI-834 checks it |

## Checksums

| Behaviour | Coverage |
|---|---|
| `checksum` and `pgmi_checksum` golden values (CRLF, BOM, no final newline, empty, comments, nested comments, dollar quotes, escapes, `U&`, non-ASCII, keyword case); reformatting keeps `pgmi_checksum`, a literal change does not | ✓ `checksums/golden` (a one-byte mutant fails it) |
| a database deployed by one binary redeploys with another with no run-once drift | ✓ `checksums/cross-binary-redeploy`, `checksums/cross-binary-redeploy-advanced` (set `PGMI_REFERENCE_BIN`; a one-byte mutant fails both) |

## Regex dialect

| Behaviour | Coverage |
|---|---|
| `.pgmiignore` patterns where RE2 and Rust `regex` differ (`&&`, `--` in classes) | ✓ `dialect/pgmiignore` |
| `CALL pgmi_test(...)` expansion (any case, schema-qualified, pattern), and none inside comments, strings and dollar quotes | ✓ `dialect/pgmi-test-macro`, `execution/pgmi-test-*` |

## File formats

| Format | Coverage |
|---|---|
| `.env` edge cases | ✓ `formats/dotenv-edges` |
| `--params-file` edge cases; refuses `\` or `$` in double quotes and an unquoted ` #` (10) | ✓ `formats/params-file-edges` |
| `pgmi.yaml` edge cases (`~`, numbers, `5432.0`, booleans, folded and literal blocks) | ✓ `formats/pgmi-yaml-edges` |

## Wizards

| Behaviour | Coverage |
|---|---|
| `init` wizard, overwrite confirmation (right name / wrong name = 12), `--force` countdown and banner, `config` | ✓ `terminal/*` |

## Cloud sign-in

| Provider | Coverage |
|---|---|
| Azure (each credential choice, flag over env, failure hint) | → PGMI-853 |
| AWS (token shape, region choice) | → PGMI-853 |
| Google Cloud SQL | → PGMI-853 |
| Lakebase (token as password) | → PGMI-853 |

## Contract notes

Oddities found while writing cases. Go is frozen, so they are recorded here and
decided in the behaviour contract (PGMI-828), not fixed.

- `sslmode=allow` never retries with TLS: against a server that requires TLS it
  fails (`tls/tls-only-and-plain-servers`). libpq retries. Decide whether Rust
  copies pgx or libpq.
- An unknown connection-string key is sent to the server as a setting and
  refused there, exit 11 (`connection/keywords`). libpq refuses it before
  connecting. Same decision.
- `PGPORT` must be one integer: `PGPORT=5432,5433` is exit 10
  (`connection/target-session-attrs`). libpq accepts a list matching `PGHOST`.
- A wrong `sslpassword` is exit 10 (configuration), not 11
  (`tls/encrypted-client-key`).
- When a host name resolves to several addresses, only the last address's error
  is reported: a certificate failure on 127.0.0.1 shows as "connection refused"
  from `::1` (`tls/system-roots`, seen on GitHub runners). The Rust version should
  report every address's error.
- A POSIX class such as `[[:space:]]` in `.pgmiignore` is refused as an invalid
  pattern, exit 10 (`dialect/pgmiignore-posix-class`); git accepts it. A Go bug
  left in place by the freeze; the Rust version should accept it.
