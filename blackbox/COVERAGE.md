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
| `init` wizard (terminal, no `-t`) | → PGMI-852 |
| `config` (wizard, needs a terminal) | → PGMI-852 |
| `metadata plan` (+`--json`) | ✓ `commands/metadata`, `commands/metadata-invalid` |
| `metadata validate` (+`--json`), bad id = 10, duplicate id = 10 | ✓ `commands/metadata`, `commands/metadata-invalid`, `commands/metadata-duplicate` |
| `metadata scaffold` (preview, `--write`, `--sort-key`) | ✓ `commands/metadata`; `--idempotent=false` → PGMI-852 |
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
| `--sslmode`, `--sslcert`, `--sslkey`, `--sslrootcert` | → PGMI-854 |
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
| `PGPASSFILE`, `~/.pgpass` | → PGMI-854 |
| `PGSERVICE` and `service=` refused | ✓ `env/pgservice-refused` |
| `PGSSLMODE`, `PGSSLCERT`, `PGSSLKEY`, `PGSSLROOTCERT`, `PGSSLPASSWORD` | → PGMI-854 |
| `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGREQUIREAUTH`, `PGCHANNELBINDING` (read by the driver) | → PGMI-854 |
| `PGHOSTADDR`, `PGSSLCRL` (ignored) | → PGMI-854 |
| `NO_COLOR`, `PGMI_NO_BANNER`, `CI`, `PGMI_NON_INTERACTIVE` | → PGMI-852 (each changes output only on a terminal) |
| `PGMI_PAGER`, `PAGER` | → PGMI-852 (paging applies on a terminal only) |
| `AZURE_*`, `AWS_REGION`, `AWS_DEFAULT_REGION` | → PGMI-853 |

## Project configuration

| Source | Coverage |
|---|---|
| `pgmi.yaml` connection, params, timeout; unknown key = 10 | ✓ `config/pgmi-yaml`, `config/pgmi-yaml-invalid` |
| precedence: flags > environment > `pgmi.yaml` > defaults, with the `connection: from ...` line | ✓ `config/precedence`, `config/pgmi-yaml`, `env/connection-string-vars` |
| project `.env` loaded first, set variables win, `dotenvKeys` in `--json` lists only the keys it set | ✓ `config/dotenv` |
| `.pgmiignore` | → PGMI-852 |

## Connection layer

The 41 cases in the driver research (`research-pg-driver.md`, "Black-box parity
tests"): every sslmode incl. fallback, `~/.postgresql/root.crt`,
`sslrootcert=system`, direct SSL negotiation, channel binding, multi-host and
`target_session_attrs`, the joined error, unknown connection-string keys,
NOTICE order, cancel on signal and timeout, transaction-status outcomes.
→ PGMI-854

## Checksums

| Behaviour | Coverage |
|---|---|
| `checksum` and `pgmi_checksum` golden values (CRLF, BOM, no final newline, non-ASCII, comments, keyword case) | → PGMI-852 |
| a database deployed by one binary redeploys with another with no run-once drift | → PGMI-852 |

## Regex dialect

| Behaviour | Coverage |
|---|---|
| `.pgmiignore` patterns where RE2 and Rust `regex` differ | → PGMI-852 |
| `CALL pgmi_test(...)` expansion, and no expansion inside comments, strings and dollar quotes | → PGMI-852 |

## File formats

| Format | Coverage |
|---|---|
| `.env` edge cases | → PGMI-852 |
| `--params-file` edge cases | → PGMI-852 |
| `pgmi.yaml` edge cases | → PGMI-852 |

## Wizards

| Behaviour | Coverage |
|---|---|
| `init` wizard, overwrite confirmation (yes / no), `config` keeps comments in pgmi.yaml | → PGMI-852 |

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

(none yet)
