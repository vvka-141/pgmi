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
| 15 | concurrent deploy; concurrent `--overwrite`; a deploy started during the overwrite approval | ✓ `exit-codes/15-concurrent-deploy`, `flags/overwrite-concurrent`, `flags/overwrite-deploy-during-approval` |
| 16 | `--timeout` exceeded | ✓ `exit-codes/16-timeout`; the cancelled statement's file, phase, SQLSTATE and WHERE in `--json` ✓ `cancel/stopped-record` |
| 130 | interrupted | ✓ `exit-codes/130-interrupted` (SIGINT), `exit-codes/130-sigterm`; the cancelling line and the cancelled statement's record ✓ `cancel/stopped-record` |

## Commands

| Command | Coverage |
|---|---|
| `deploy` | ✓ `exit-codes/*`, `deploy/*`, `params/*`; flags below |
| `info`, `info --json`, missing path = 10 | ✓ `commands/info` |
| `init` (no `-t` without a terminal = minimal; `-t basic\|advanced`; manifests written without `.scaffold`; non-empty target = 2; unknown template = 2; unwritable target = 1; unlistable target = 1, nothing written) | ✓ `commands/init`, `commands/init-unwritable`, `commands/init-unlistable` |
| `init` wizard (terminal, no `-t`) | ✓ `terminal/init-wizard`, `terminal/init-wizard-connection`, `terminal/init-wizard-connection-esc`, `terminal/init-wizard-keeps-unparseable-yaml` |
| `config` (wizard: tests the connection, keeps pgmi.yaml comments and params, saves to `~/.pgpass`) | ✓ `terminal/config-wizard`, `terminal/config-refusals`, `terminal/config-replace-unparseable` |
| `metadata plan` (+`--json`) | ✓ `commands/metadata`, `commands/metadata-invalid` |
| `metadata validate` (+`--json`), bad id = 10, duplicate id = 10 | ✓ `commands/metadata`, `commands/metadata-invalid`, `commands/metadata-duplicate` |
| `metadata scaffold` (preview, `--write`, `--sort-key`, `--idempotent=false`) | ✓ `commands/metadata`, `commands/metadata-scaffold-run-once` |
| `metadata scaffold` key derivation (no key in a directory without keys, first key of each sibling, hint names the directory); `validate` skips `__test__/`; `plan` duplicate ids unprefixed | ✓ `commands/metadata-scaffold-and-checks` |
| `templates list`, `templates describe <t>`, unknown = 2 | ✓ `commands/templates` |
| basic template on a host whose provider owns PUBLIC-executable functions in `public` (Lakebase) deploys | ✓ `deploy/basic-template-provider-functions` |
| advanced template: first-deploy check of the login's attributes and Azure's extension list | ✓ `deploy/advanced-template-preflight` |
| `version`, `--version`, extra argument = 2 | ✓ `commands/version` |
| `ai`, `ai contract`, unknown subcommand = 2 | ✓ `commands/ai` |
| `completion bash\|zsh\|fish\|powershell`, other shell = 2 | ✓ `commands/completion`; PowerShell template and sslmode values ✗ (the scripts differ by design; Rust unit test `powershell_completes_template_names_and_sslmode_values`, checked by hand with `pwsh`) |
| `help`, `--help`, `<command> --help`, unknown command / wrong arity = 2 | ✓ `commands/help`, `commands/deploy-help-text` |
| `-v` / `--verbose` (DEBUG notices shown, none from pgmi's own queries; parameter counts, never keys or values) | ✓ `commands/verbose`, `notices/verbose-no-internal-debug`, `params/verbose-counts` |
| `serve`: initialize, `tools/list` (7 tools), a tool call, malformed message (-32700, session continues), unknown method (-32601), EOF exits 0 | ✓ `serve/session` |
| `serve`: a client that reads while it pipelines 3000 requests gets every answer in order | ✓ `serve/burst` |
| `serve`: `deploy` tool with params, ping answered during a deploy, `notifications/cancelled` rolls the deploy back | ✓ `serve/deploy-tool`, `serve/cancel`; WARNINGs in the text block ✓ `serve/deploy-warning-lines` |
| `serve`: malformed input (not JSON, non-object, `jsonrpc`, id types, unknown method and tool, bad params, blank line, 2 MB line, invalid UTF-8) | ✓ `serve/malformed`; the bare value `null` (no answer) ✗ deliberately unpinned, see `rust/docs/decisions/go-review.md` |
| `serve`: protocol versions, `server/discover`, the 2026-07-28 stateless era | ✓ `serve/protocol-versions` |
| `serve`: no `.env`, overwrite needs `confirmDatabaseName` (12), unknown argument (10) | ✓ `serve/deploy-guards` |
| `serve`: a `params` key that is not an identifier (10); a `PASSWORD` literal masked in the text, `message` and `error` | ✓ `serve/deploy-param-key`, `serve/deploy-password-masked` |
| `serve`: deploys in sequence after an open transaction or a failed tail block; overlapping deploys = 15 | ✓ `serve/deploys-in-sequence`, `serve/concurrent-deploys` |
| `serve`: deploy `timeout` argument (16, not a duration = 10) | ✓ `serve/deploy-timeout` |
| `init`: `{{PROJECT_NAME}}`, JSON-escaped in `.json` | ✓ `commands/init-project-name`; file modes 0644/0755 ✗ (the runner checks content only) |
| `pgmi` with no arguments | ✓ `commands/bare` |
| `pgmi: error:` prefix, unknown subcommand and shorthand, `-h` on deploy, several cloud flags = 10 | ✓ `commands/usage-errors` |
| `-h` is help (exit 0) on every command but deploy; an invalid `--sslmode` value = 10 | ✓ `commands/short-help-and-sslmode-value` |
| metadata id spellings, XML leniency, fallback id; broken blocks = 10 | ✓ `commands/metadata-forms`, `commands/metadata-refusals` |
| summary line format; `-v` elapsed prefix on notices | ✓ `notices/summary-line`, `notices/verbose-elapsed` |

## Deploy flags

| Flag | Coverage |
|---|---|
| `--connection` URI | ✓ every `deploy` case |
| `--connection` keyword/value, ADO.NET | ✓ `flags/connection-forms` |
| `-h`, `-p`, `-U`, `-d`; `-h` with `--connection` = 10 | ✓ `flags/granular-flags` |
| `--compat` (unknown = 10 with the target in the summary and `--json`) | ✓ `flags/compat`, `flags/compat-record` |
| `--force` (alone; with `--overwrite`, no countdown without a terminal) | ✓ `flags/overwrite-force` |
| `--overwrite` without `--force`, no terminal | ✓ `exit-codes/12-overwrite-denied` |
| `--overwrite` restores owner, connection limit, settings, role settings, comment, privileges; refuses (10) what it cannot restore | ✓ `flags/overwrite-restores` |
| `--overwrite` of an invalid database (connection limit -2) recreates it without a limit | ✓ `flags/overwrite-invalid-database` |
| `--json` | ✓ `deploy/json-success`, `deploy/json-failure` |
| `--param` (never split on commas) | ✓ `params/precedence`, `commands/usage-errors` |
| `--params-file` (file, `-` stdin, later file wins, case clash = 10, `-` twice = 10, missing file = 10); `--param` without `=` = 10 | ✓ `params/precedence`, `flags/params-file` |
| `--timeout` (`3s`, `90s`, `1h30m`, `0`; not a duration, or longer than a Go duration, = 2) | ✓ `exit-codes/16-timeout`, `flags/timeout-forms`, `commands/usage-errors` |
| `--sslmode`, `--sslcert`, `--sslkey`, `--sslrootcert`; `--sslmode` overrides `DATABASE_URL`'s, refused next to `--connection` (10) | ✓ `tls/client-certificate`, `tls/sslmode-flag-overrides` |
| `--azure`, `--azure-client-id` | ✓ `cloud/azure-cli`, `cloud/azure-managed-identity`; `--azure-tenant-id` > `AZURE_TENANT_ID` > `azure_tenant_id`, and `AWS_REGION`/`AWS_DEFAULT_REGION` > `aws_region` ✓ `cloud/cloud-ids-env-beats-yaml` |
| `--aws`, `--aws-region` | ✓ `cloud/aws-iam-token` |
| `--google`, `--google-instance` | ✓ `cloud/google-arguments` |

## Environment

| Variable | Coverage |
|---|---|
| `PGMI_PARAM_<NAME>` | ✓ `params/env-var-reaches-session`, `params/precedence`, `exit-codes/10-invalid-config` |
| `PGMI_CONNECTION_STRING`, `DATABASE_URL` (and their order, and the `connection: from ...` line) | ✓ `env/connection-string-vars` |
| `-h`/`-p`/`-U` next to `PGMI_CONNECTION_STRING` or `DATABASE_URL` = 10 | ✓ `env/connection-string-vars-refuse-flags` |
| a connect or resolve failure ends with `(connection from <source>)` / `(connection defaults)` unless typed | ✓ `errors/connection-source` |
| `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, `PGAPPNAME`, `PGCONNECT_TIMEOUT` | ✓ `env/pg-variables`, `env/pgappname`, `env/pgconnect-timeout` |
| `PGPASSFILE`, `~/.pgpass` | ✓ `connection/pgpass*` |
| `PGSERVICE` and `service=` refused | ✓ `env/pgservice-refused` |
| `PGSSLMODE`, `PGSSLCERT`, `PGSSLKEY`, `PGSSLROOTCERT`, `PGSSLPASSWORD` | ✓ `tls/client-certificate`, `tls/encrypted-client-key` |
| `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGREQUIREAUTH`, `PGCHANNELBINDING` (read by the driver) | ✓ `connection/keywords`, `connection/target-session-attrs`, `tls/require-auth-and-version`, `tls/channel-binding-and-direct` |
| `PGHOSTADDR`, `PGSSLCRL` (ignored) | ✓ `env/ignored-variables` |
| `PGSSLSNI`, `PGSSLCERTMODE`, `PGMINPROTOCOLVERSION`, `PGMAXPROTOCOLVERSION` (invalid = 10) | ✓ `env/pg-tls-protocol-variables` |
| `NO_COLOR`, `PGMI_NO_BANNER`, `CI`, `PGMI_NON_INTERACTIVE` (on a terminal) | ✓ `terminal/colors`, `terminal/force-banner`, `terminal/non-interactive` |
| no terminal queries outside the wizards | ✓ `terminal/no-terminal-queries` |
| `PGMI_PAGER`, `PAGER`, `PGMI_PAGER=cat` (on a terminal); split without a shell, default `less -R`, a pager that cannot start | ✓ `terminal/pager`, `terminal/pager-command` |
| `PGMI_MAX_FILE_SIZE` (cap, not a number = 10) | ✓ `commands/metadata-refusals` |
| `IDENTITY_ENDPOINT`/`IDENTITY_HEADER`, `AWS_REGION`, `AWS_DEFAULT_REGION`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` | ✓ `cloud/*`; `AZURE_TENANT_ID`, `AZURE_CLIENT_SECRET` ✗ (they only reach Entra, see Cloud sign-in) |

## Project configuration

| Source | Coverage |
|---|---|
| `pgmi.yaml` connection, params, timeout; unknown key or a timeout too long = 10 | ✓ `config/pgmi-yaml`, `config/pgmi-yaml-invalid`, `config/pgmi-yaml-timeout-too-long` |
| precedence: flags > environment > `pgmi.yaml` > defaults, with the `connection: from ...` line | ✓ `config/precedence`, `config/pgmi-yaml`, `env/connection-string-vars` |
| project `.env` loaded first, set variables win, `dotenvKeys` in `--json` lists only the keys it set | ✓ `config/dotenv` |
| `.env` named on stderr: `(set by <path>)` on the connection line, `<path> applied: <keys>` (never values; `./` for a relative project path) | ✓ `config/dotenv-provenance` |
| a `PG*` variable or connection string replacing a `pgmi.yaml` connection value (database, host, port, user, sslmode, certificates) named on stderr and in `--json` `overrides`; a string dropping `pgmi.yaml`'s sslmode; `session options: from PGOPTIONS` | ✓ `config/overrides` |
| `.env` `$NAME` expansion, a broken `.env` warned about; `.env` reaches the cloud sign-in | ✓ `formats/dotenv-expansion`, `cloud/dotenv-feeds-sdk` |
| project walk: symbolic links, NUL bytes, invalid UTF-8, `__name__` directories | ✓ `dialect/project-walk`, `commands/metadata-refusals` |
| project walk: a named pipe is refused (10), never read, by `deploy` and `info` | ✓ `errors/special-file` |
| `.pgmiignore` (globs, classes, `**`, directories, `!`, escaped `#`; hiding deploy.sql = 10; bytes that are not UTF-8 read leniently) | ✓ `dialect/pgmiignore`, `dialect/pgmiignore-hides-deploy`, `dialect/pgmiignore-posix-class`, `dialect/pgmiignore-latin1`; empty or reversed classes = 10, an unclosed `[` literal ✓ `dialect/pgmiignore-broken-patterns` |

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
| 7a | session tables analysed after loading (planner statistics) | ✓ `deploy/session-tables-analyzed` |
| 8 | PGlite single-session server | ✗ needs a PGlite server; the Go integration test against PGlite and the manual check in `docs/CONNECTIONS.md` cover it |
| 9 | NOTICE streamed while the statement runs | ✓ `notices/streaming` |
| 10 | severities, DETAIL, HINT, LOG hidden | ✓ `notices/severity`; localized server messages ✗ (no non-English locale in the image) |
| 11 | a notice before an error prints before it | ✓ `notices/severity` |
| 12 | syntax error in deploy.sql | ✓ `errors/head-syntax` |
| 13, 14 | error inside an executed file names the file | ✓ `errors/attribution` |
| 15 | `--json` failure record: `sqlstate`, `failedFile` | ✓ `errors/attribution` |
| 15a | the connection password never reaches `--json` through DETAIL, HINT or CONTEXT | ✓ `errors/diagnostics-secrets` |
| 15b | the connection password printed in a notice, an error, `--json` or a serve answer is `***` (Rust; Go prints it), also when it came from `~/.pgpass` | ✓ `notices/secrets-redacted`, `notices/pgpass-secret-redacted` |
| 16 | 42501 TEMPORARY guidance; wrong password; missing maintenance database; hba reject | ✓ `errors/temporary-privilege`, `errors/connection-failures`, `tls/tls-only-and-plain-servers`; 53300, 57P03 ✗ (server states a case cannot produce reliably) |
| 17 | `--timeout` cancels on the server, no backend left | ✓ `cancel/timeout-cancels` |
| 18 | cancel over TLS and over direct TLS negotiation | ✓ `cancel/timeout-over-tls` |
| 19 | cancel request blocked by a firewall | ✗ needs a firewall between client and server; `internal/db/cancel_integration_test.go` covers it |
| 20 | SIGINT twice = 130 at once | ✓ `cancel/sigint-twice` |
| 21 | unreachable host with a connect timeout = 11, not 16; a timeout beyond Go's range | ✓ `env/pgconnect-timeout`, `connection/connect-timeout-range` |
| 22 | URI, keyword/value and ADO.NET forms; a string that does not parse is not echoed; percent escapes, URI fragments, an option after a password | ✓ `flags/connection-forms`, `errors/connection-string-secrets`, `errors/connection-string-password-rest` |
| 23 | `PG*` variables, `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGCHANNELBINDING`, `PGREQUIREAUTH` | ✓ `env/pg-variables`, `connection/keywords`, `connection/target-session-attrs`, `tls/channel-binding-and-direct`, `tls/require-auth-and-version` |
| 24 | `.pgpass`, wildcard, `PGPASSFILE`, wrong entry, world-readable file ignored | ✓ `connection/pgpass`, `connection/pgpass-wildcard-and-file`, `connection/pgpass-world-readable` |
| 25 | `service=` / `PGSERVICE` refused | ✓ `env/pgservice-refused` |
| 26 | multi-host: a down first host is skipped; both down names both | ✓ `connection/multi-host` |
| 27 | `target_session_attrs`, all six values, against a primary and a standby | ✓ `connection/target-session-attrs` |
| 28 | `options=` applied; unknown key sent to the server and refused (exit 11) | ✓ `connection/keywords` |
| 28a | `max_protocol_version=3.2` deploy and cancel; `sslsni`, `sslcertmode`, `client_encoding` accepted | ✓ `connection/protocol-version` |
| 29 | Unix socket directory, with and without a port | ✓ `connection/unix-socket` |
| 29a | libpq keys Go sends to the server (keepalives, `tcp_user_timeout`, `load_balance_hosts`, `hostaddr`, GSSAPI keys, `sslcompression`, `client_encoding`) and the pgx options' values | ✓ `connection/keepalives`, `connection/tcp-user-timeout`, `connection/load-balance-hosts`, `connection/hostaddr`, `connection/gssapi-keys`, `connection/sslcompression`, `connection/client-encoding`, `connection/pgx-options` |
| 29b | keyword/value with spaces around `=` and a `;` in a quoted value; ADO.NET quoted values; ADO.NET `Host=h:port` beats `Port=` | ✓ `connection/string-forms` |
| 30 | every sslmode; `~/.postgresql/root.crt`; `require` + root = verify-ca; `pgmi.yaml`'s certificate paths apply beside a connection string | ✓ `tls/sslmode-matrix`, `tls/sslmode-refusals`, `tls/default-files`, `tls/default-files-good-root`, `tls/yaml-certificates-with-string` |
| 31 | hostname mismatch: verify-ca passes, verify-full fails | ✓ `tls/sslmode-matrix`, `tls/sslmode-refusals` |
| 32 | `sslrootcert=system`, with `SSL_CERT_FILE`; it makes `verify-full` the default | ✓ `tls/system-roots` |
| 33 | server without TLS: prefer plaintext, require refused; TLS-only server: allow refused | ✓ `tls/tls-only-and-plain-servers` |
| 34 | `sslnegotiation=direct` | ✓ `tls/channel-binding-and-direct`; failure against PostgreSQL 16 ✗ (one server version per run) |
| 35 | client certificate by flags, by `PGSSL*`, by `~/.postgresql/postgresql.crt`; encrypted key with `sslpassword`/`PGSSLPASSWORD`; wrong password = 10; half a pair = 10; `disable` reads neither | ✓ `tls/client-certificate`, `tls/client-certificate-pairs`, `tls/default-files-good-root`, `tls/encrypted-client-key`, `tls/encrypted-pkcs8-client-key` |
| 36 | channel binding with an RSA certificate; refused without TLS | ✓ `tls/channel-binding-and-direct`; ECDSA and Ed25519 certificates ✗ (one server certificate per run) |
| 37 | `require_auth` refuses another method; a mistyped method is 10 before any host lookup | ✓ `tls/require-auth-and-version` |
| 38 | `--json` TLS version | ✓ `tls/require-auth-and-version` |
| 39, 40 | token callbacks per dial, Cloud SQL dialer | ✗ in-process hooks; the cloud cases (PGMI-853) cover them from outside |
| 41 | static musl build | ✗ a property of the Rust build; PGMI-834 checks it |

## Checksums

| Behaviour | Coverage |
|---|---|
| `checksum` and `pgmi_checksum` golden values (CRLF, BOM, no final newline, empty, comments, nested comments, dollar quotes, escapes, `U&`, non-ASCII, keyword case, Unicode white space at the ends and inside); reformatting keeps `pgmi_checksum`, a literal change does not | ✓ `checksums/golden` (a one-byte mutant fails it) |
| a database deployed by one binary redeploys with another with no run-once drift | ✓ `checksums/cross-binary-redeploy`, `checksums/cross-binary-redeploy-advanced` (set `PGMI_REFERENCE_BIN`; a one-byte mutant fails both) |

## Regex dialect

| Behaviour | Coverage |
|---|---|
| `.pgmiignore` patterns where RE2 and Rust `regex` differ (`&&`, `--` in classes) | ✓ `dialect/pgmiignore` |
| `CALL pgmi_test(...)` expansion (any case, schema-qualified, pattern), and none inside comments, strings and dollar quotes | ✓ `dialect/pgmi-test-macro`, `execution/pgmi-test-*` |
| comments between the macro's tokens; ASCII white space only (no NBSP, no VT) | ✓ `dialect/pgmi-test-macro-comments`, `dialect/pgmi-test-macro-whitespace` |
| a non-ASCII letter glued to `CALL` is one identifier, as in PostgreSQL (Rust; Go expands) | ✓ `dialect/call-glued-to-a-non-ascii-letter` |

## File formats

| Format | Coverage |
|---|---|
| `.env` edge cases | ✓ `formats/dotenv-edges`; a NUL byte ✓ `formats/dotenv-nul` |
| `--params-file` edge cases; refuses `\` or `$` in double quotes and an unquoted ` #` (10) | ✓ `formats/params-file-edges` |
| `pgmi.yaml` edge cases (`~`, numbers, `5432.0`, booleans, folded and literal blocks) | ✓ `formats/pgmi-yaml-edges` |

## Wizards

| Behaviour | Coverage |
|---|---|
| `init` wizard, overwrite confirmation (right name / wrong name = 12), `--force` countdown and banner, `config` | ✓ `terminal/*` |
| `--timeout` during the `--force` countdown = 16, nothing dropped | ✓ `terminal/force-countdown-timeout` |
| end of input at the prompt = 12, SIGINT at the prompt or countdown = 130, Ctrl-C in `config` = 130 | ✓ `terminal/prompt-cancel`; Esc on the first `config` screen ✗ (did not end the wizard under the runner's pty; not investigated) |

## Cloud sign-in

| Provider | Coverage |
|---|---|
| Azure through the Azure CLI (no client ID) | ✓ `cloud/azure-cli` (stub `az` on `PATH`) |
| Azure PowerShell after the CLI and azd; `AZURE_TOKEN_CREDENTIALS` picks one | ✓ `cloud/azure-powershell` (stub `pwsh` on `PATH`) |
| An `AZURE_TOKEN_CREDENTIALS` value that names no credential: refused before connecting (Go 1, Rust 10) | ✓ `cloud/azure-token-credentials-invalid` |
| Azure managed identity (`--azure-client-id`) | ✓ `cloud/azure-managed-identity` (fake App Service endpoint) |
| Azure managed identity on Cloud Shell, Azure ML, Service Fabric | ✓ `cloud/azure-cloud-shell`, `cloud/azure-ml`, `cloud/azure-service-fabric` (fake endpoints; Service Fabric's certificate is pinned by `IDENTITY_SERVER_THUMBPRINT`) |
| Azure managed identity on Azure Arc | ✗ the key file must sit in `/var/opt/azcmagent/tokens`, which a case cannot write; Rust unit tests cover the file checks |
| Azure service principal (`AZURE_CLIENT_SECRET`) | ✗ instance discovery always calls `login.microsoftonline.com`; the manual Azure acceptance check in the release checklist covers it |
| token sign-in refuses `prefer`/`disable` (10) | ✓ `cloud/token-auth-requires-tls` |
| `sslmode: prefer` from `pgmi.yaml` with token sign-in: the line and the fix (Rust) | ✓ `cloud/token-auth-pgmi-yaml-sslmode` |
| AWS IAM token shape, region from `--aws-region` / `AWS_REGION` / both, missing = 10 | ✓ `cloud/aws-iam-token` (a recording PostgreSQL front captures the token) |
| AWS profile `credential_source` (`Ec2InstanceMetadata`, `EcsContainer`) with `role_arn` | ✓ `cloud/aws-credential-source` (fake IMDS, container endpoint and STS through `AWS_ENDPOINT_URL_STS`); `Environment` has no case, since environment keys win before the profile in both SDKs |
| AWS console sign-in (`login_session`) | ✗ needs the sign-in service and its DPoP-bound cache; aws-config's `credentials-login` is enabled in Rust |
| Google: `--google-instance` required; missing credentials | ✓ `cloud/google-arguments`; the dialer: no case, since Go's connector offers no Admin API endpoint override. The `fakes` server carries a Cloud SQL Admin API (`connectSettings`, `generateEphemeralCert`, an `authorized_user` token endpoint) and a TLS 1.3 instance port on `127.0.0.x:3307` that requires the ephemeral client certificate; the Rust integration test `rust/pgmi/tests/google_cloud_sql.rs` drives the Rust binary through it (`CLOUDSDK_API_ENDPOINT_OVERRIDES_SQLADMIN`) |
| `.env` AWS keys and region reach the token | ✓ `cloud/dotenv-feeds-sdk` |
| Lakebase (a token passed as the password) | ✓ no separate path: the password and TLS cases cover it |
| Lakebase native sign-in (`--databricks`, Rust only, `impl: rust`): credential request shape, PAT, endpoint from flag or `PGMI_DATABRICKS_ENDPOINT`, one credential per deploy, no token in output or `--json` | ✓ `cloud/databricks-pat` (fake workspace API on `{fake_https}`, trusted through `SSL_CERT_FILE`) |
| Lakebase credential replaced before it expires | ✓ `cloud/databricks-refresh` (the fake answers a 60 s credential for an endpoint ending in `-short`) |
| Lakebase sign-in by OAuth M2M, the Databricks CLI (profile and host), Azure (`DATABRICKS_AUTH_TYPE=azure-cli`) | ✓ `cloud/databricks-m2m`, `cloud/databricks-cli` (stub `databricks`), `cloud/databricks-azure` (stub `az`); Azure picked by an `*.azuredatabricks.net` host has no case (the fake runs on `localhost`), Rust unit tests cover the choice |
| Lakebase refusals: endpoint, host, auth type, sslmode (10); 401, 403, 404, 503 retried, no credential source, a credential refused by the database (11) | ✓ `cloud/databricks-errors`, `cloud/databricks-azure` |

## Contract notes

The PGMI-830 review against Go adds its own list, with owner questions, in
`rust/docs/decisions/go-review.md`.

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
- An encrypted PKCS#8 client key (`openssl pkcs8 -topk8`, PBES2 or
  `-v1 PBE-SHA1-3DES`) cannot be parsed: exit 10 with or without a password
  (`tls/encrypted-pkcs8-client-key`). libpq opens it; so does the Rust binary.
- When a host name resolves to several addresses, only the last address's error
  is reported: a certificate failure on 127.0.0.1 shows as "connection refused"
  from `::1` (`tls/system-roots`, seen on GitHub runners). The Rust version should
  report every address's error.
- `--google` without Application Default Credentials exits 1 (unclassified),
  where a failed Azure or AWS sign-in exits 11 (`cloud/google-arguments`). The
  Rust version should use 11.
- A service principal (`AZURE_CLIENT_SECRET`) always validates the authority
  host against `login.microsoftonline.com` (instance discovery is on), so it
  cannot be pointed at a private or test authority. Decide whether Rust keeps
  that.
- A POSIX class such as `[[:space:]]` in `.pgmiignore` is refused as an invalid
  pattern, exit 10 (`dialect/pgmiignore-posix-class`); git accepts it. A Go bug
  left in place by the freeze; the Rust version should accept it.

## Documented promises pinned by the docs review (PGMI-879)

| Promise | Case |
|---|---|
| `CALL pgmi_test()` with no tests aborts the deploy (13) | ✓ `execution/pgmi-test-without-tests` |
| `_setup.psql` is a fixture too | ✓ `execution/setup-psql-fixture` |
| `info` names the version and template that scaffolded the project | ✓ `commands/info-scaffolded-by` |
| `info` refuses an unparseable `<pgmi-meta>`; `info` and `metadata plan`, `validate`, `scaffold` refuse a `.pgmiignore` hiding deploy.sql (10) | ✓ `commands/info-metadata-refusals` |
| the maintenance database is dialed only when the target is missing | ✓ `connection/maintenance-database-only-when-needed` |
| `passfile=` in the string beats `PGPASSFILE` | ✓ `env/passfile-beats-pgpassfile` |
| Ctrl-C with `--json`: `exitCode` 130 | ✓ `cancel/ctrl-c-json-exit-code` |
| a concurrent index cut off by `--timeout`: exit 16, INVALID index left | ✓ `cancel/timeout-leaves-invalid-concurrent-index` |
| a nested `deploy.sql` is loaded; `--verbose` names ignored paths | ✓ `dialect/nested-deploy-sql-loaded` |
| a parameter value never reaches the statement text pgmi echoes on an error | ✓ `errors/parameter-value-not-in-statement-echo` |
| a client ID without a secret signs in as that identity, never through the Azure CLI | ✓ `cloud/azure-client-id-never-uses-the-cli` |
| AWS temporary credentials (`AWS_SESSION_TOKEN`) and a named profile (`AWS_PROFILE`) sign the RDS token | ✓ `cloud/aws-session-token-and-profile` |
