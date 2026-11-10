# Releases

> **Writing a release?** Add the section here *before* tagging — CI lifts it to the
> top of the GitHub release page. The tag build fails if the section is missing or
> empty, if the heading carries no `YYYY-MM-DD` date, or if a `<!-- DRAFT -->`
> marker still sits above it, so finish the notes and date the heading as the last
> step before you tag. The shape and the rules are in
> [`.github/RELEASE_NOTES_TEMPLATE.md`](.github/RELEASE_NOTES_TEMPLATE.md).
> Write for a stranger who arrived from a search result.
>
> Before anything publishes, the snapshot build runs `scripts/verify-walkthrough.sh`
> against a GoReleaser build of the tagged commit — the same config the release
> job publishes with: the README "See it work" blocks as written, its failure
> demo, and the Quickstart steps. Run it locally with
> `scripts/verify-walkthrough.sh <binary>` (Docker required). About ten minutes
> after the release publishes, and weekly, `verify-install.yml` installs it
> through every documented channel and checks each serves the new version — look
> at that run before announcing a release.

## v0.14.1 — 2026-11-09

**You can now deploy the advanced template to Databricks Lakebase, from your machine or from a serverless job in a Databricks bundle.** Lakebase grants its own roles on every schema and table it sees created; two deploy parameters name those roles, the deploy takes back what row-level security cannot hold, and the template's grant checks pass. This was verified on a real Lakebase project. The refusal log and the request log now sit behind row-level security too, so a provider's reader sees no organization's data in them. A new page, [Databricks Lakebase](docs/LAKEBASE.md), walks through the credential, the bundle job and a test-gated deploy on a Lakebase branch.

The rest is fixes: clearer errors for refused cloud tokens, keyword connection strings that work against single-session servers such as PGlite, and docs that no longer read as if a deploy must be one transaction. Advanced-template projects: read Upgrading first.

### New

- **An MCP tool in the advanced template can propose a write for a person to confirm.** A tool declared with `"effect": "proposal"` names its target write tool in `"proposes"`; calling it records the proposal and changes nothing else. A person signed in as themselves with `proposals.decide` lists pending proposals with `GET /proposals` and confirms or rejects one; confirming runs the target tool as that person, under that person's permission ([MCP handlers](docs/advanced/MCP-HANDLERS.md)).
- **The advanced template deploys on Databricks Lakebase and other providers that grant their own roles on every new schema and table.** Two deploy parameters name what the provider adds: `provider_role_pattern` (Lakebase: `databricks\_%`) and `provider_schemas` (Lakebase: `["__db_system"]`). Every deploy then revokes the provider roles' `CREATE` on the template's schemas and their whole-table privileges on its tables, and the grant guards leave out only what row-level security still binds; grants to `PUBLIC` and to the template's roles still fail the deploy. The refusal log `api.access_denial` and the exchange log `api.exchange` are now forced to row-level security, so a provider's reader granted `SELECT` on them sees no organization's refusals or logged requests ([production guide](docs/PRODUCTION.md#grants-a-provider-makes-on-its-own)). A new page, [Databricks Lakebase](docs/LAKEBASE.md), covers the credential, deploying from a serverless bundle job and a test gate on a Lakebase branch.

### Fixes

- **A refused Azure, AWS or Google token names the provider.** The error now points at the database role and `--username` instead of suggesting `$PGPASSWORD` and `~/.pgpass`, which play no part in token authentication ([connections](docs/CONNECTIONS.md#azure-entra-id)).
- **`/rpc` answers a JSON-RPC notification with 202 and no body, and runs nothing.** A body with no `id` member is a notification; `"id": null` is still a request and gets an answer ([gateway protocol](shared/templates/advanced/gateways/PROTOCOL.md)).

- **A keyword/value connection string accepts pgx's `default_query_exec_mode`, `statement_cache_capacity` and `description_cache_capacity`,** as a URI already did. With `default_query_exec_mode=exec`, a second deploy to a server that keeps one session for every connection, such as the PGlite socket server, no longer fails with `prepared statement "stmtcache_..." already exists` ([connections](docs/CONNECTIONS.md#connection-string-formats)).
- **The Go reference gateway logs why it could not connect.** A refused password, a TLS mismatch, a bad certificate or an unknown host used to look like pool exhaustion: the log said only "no database connection is free". The 503 the client gets is unchanged; the log line now carries the connect error, without the password ([Go gateway](shared/templates/advanced/gateways/go/README.md)).
- **`pgmi metadata scaffold` on a directory inside a project scans from the project root.** Its `--sort-key ./` hint used to reorder a basic project's `semantic/` files ahead of `physical/` and break the deploy, and it ignored the root `.pgmiignore`. Paths, the suggested key and `.pgmiignore` now match scaffolding the whole project, and `--write` names each file it changes ([CLI reference](docs/CLI.md#pgmi-metadata-scaffold)).
- **The scaffolded skills' unindexed-foreign-key audit no longer counts an `INCLUDE` column as indexing a key.** A composite foreign key whose second column sat only in `INCLUDE` was reported as indexed. The advanced template's `AGENTS.md` sends "add a table" to one place, and several skill descriptions trigger more precisely ([agent guidance](docs/core-vs-template.md)).
- **`pgmi init` and `pgmi info` tell the truth about the template.** `pgmi info` names the template from the line `pgmi init` writes at the top of `deploy.sql`, and says `unknown` for a project without one, instead of calling every project with a `deploy.sql` "basic". `pgmi init <path>` no longer asks for the directory again, the wizard no longer promises a pgmi.yaml the minimal template does not write, and a wizard-written pgmi.yaml holds one `username:` line. For advanced, the "Next:" command deploys to `myapp_dev` as the advanced README does, and the managed-cloud note lists `btree_gist` ([CLI reference](docs/CLI.md)).
- **The Azure and AWS examples say what a fresh project needs.** The pgmi.yaml `pgmi init` writes sets `sslmode: prefer`, which token authentication refuses; [connections](docs/CONNECTIONS.md#tls-with-token-authentication) now says to pass `--sslmode verify-full`. The Python gateway's quick start creates a virtual environment first, since stock Ubuntu refuses a system-wide `pip install`.
- **A failing test in a project whose `deploy.sql` never runs tests is no longer silent.** When `__test__/` files are loaded but `deploy.sql` never mentions `pgmi_test`, the deploy says how many test files it loaded and that none run. pgmi still runs only what `deploy.sql` says ([testing](docs/TESTING.md)).
- **`deploy --json` carries the same result keys on every exit code.** A run that stopped before loading the project (exit 10, 11, 14) now reports `filesLoaded`, `database`, `created` and the rest as `null` instead of leaving them out; diagnostics still appear only when known ([CLI reference](docs/CLI.md#--json-envelope)).
- **A quoted password is redacted whole** in errors, `--json` and the MCP tools: also an `E'...'` value, a `password => '...'` argument and a JSON `"password": "..."` member. **`install.sh` reports the binary it installed** and warns when another `pgmi` earlier on PATH would still run. **The `--overwrite --force` banner** fits its frame for any database name.
- **The Python reference gateway ignores a query string on `/auth/logout`, `/auth/dev-login` and `/health`.** `POST /auth/logout?x=1` answered 404 and left the session open; it now clears both cookies and closes the session, as the other gateways do ([gateway protocol](shared/templates/advanced/gateways/PROTOCOL.md)).
- **`pgmi ai` opens with what pgmi gives a coding agent.** The overview names the three ideas in order (your SQL runs the deploy, a test tree can gate the commit, agents get tools and project skills), says the test gate holds only where `deploy.sql` calls it before `COMMIT`, and keeps pgmi core, the templates and agent tooling apart, with the advanced template's runtime MCP named as separate from `pgmi serve` ([core vs. template](docs/core-vs-template.md)).

### Upgrading

- **pgmi itself and the basic template:** no action needed.
- **Advanced-template projects** merge the template changes as usual ([Upgrading an advanced-template project](docs/advanced/UPGRADING.md)). Two new run-once files come with them, `lib/api/02a-access-control-rls.sql` and `lib/api/06a-exchange-rls.sql`; `api/admin.sql` and `api/views.sql` depend on the second, so take them together. On a kept database, `06a-exchange-rls.sql` adds two stored columns to the exchange log, which rewrites `api.exchange` and its three child tables under an exclusive lock inside the deploy: requests wait until it finishes. Purge old exchanges first (`POST /admin/maintenance/purge-exchanges`) when the log is large. The new columns cast `organizationId` and `actualPrincipalId` from the logged context to `uuid`, so a replaced `internal.request_log_context()` must store uuids there.

## v0.14.0 — 2026-11-06

**A project you scaffold with `pgmi init` now teaches the AI assistant working in it how to build a PostgreSQL application: `AGENTS.md` and a set of skills ship inside the project, next to the code they describe.** Every user also gets secrets passed through the environment instead of the command line, a deploy failure that names its file and line, and pgmi as a Go library. The basic template is redesigned around two layers. The advanced template is rebuilt as a complete application stack, in which an AI agent calling the API over MCP has its own identity.

**Decide before you upgrade an advanced-template project: v0.14 needs a new database.** It refuses a database built by v0.13 or earlier, and there is no in-place upgrade. Read [the upgrade guide](docs/advanced/UPGRADING.md#from-v013-to-v014-a-new-database) first. The advanced template is pre-1.0, so a later release may need a new database again, and template fixes reach your project by manual merge. pgmi itself and the basic template need no new database.

### Security

- **Upgrade if you deploy with Azure or AWS token authentication.** v0.13 could send the IAM token over a connection without TLS, because token authentication inherited `sslmode=prefer`. v0.14 refuses that before connecting, in the CLI and in the Go library ([connections](docs/CONNECTIONS.md)).
- **Passwords stay out of pgmi's output.** pgmi redacts SQL `PASSWORD '...'` literals, ADO.NET `Pwd=` values and quoted passwords from its errors, `--json` and the MCP tools, and `DeploymentConfig` prints without its secrets. The advanced template's admin-role password no longer reaches stderr, `--json` or the server log when its role statement fails ([security](docs/SECURITY.md#your-sql-controls-its-own-logging)).
- **A password file others can read is ignored**, with libpq's warning. pgmi checks the file the driver actually read: `passfile=`, then `PGPASSFILE`, then `~/.pgpass`.
- **Built with pgx 5.12.0**, which fixes two upstream advisories (GHSA-j38q-h3ph-jjpq, GHSA-95fj-3fxw-g62g). govulncheck found pgmi unaffected before and after, but the second matters if you rely on `require_auth` or `channel_binding=require`.

### What you can do now

Scaffold a project, open it in your coding assistant and ask for a feature:

```bash
pgmi init myapp --template basic
cd myapp
ls AGENTS.md .agents/skills
# AGENTS.md  pgmi-debug-deploy  pgmi-design  pgmi-layers  pgmi-metadata  pgmi-sql
#            pgmi-testing  pgmi-workflow  postgres-analytics  postgres-concurrency  ...
pgmi deploy . -d myapp --param env=dev
```

`AGENTS.md` tells the assistant which skill to read before which change: tables and business rules, deploy.sql, tests, a failed deploy, sort keys. The `postgres-*` skills are a recipe library for PostgreSQL's own features (partial unique and exclusion constraints, `SKIP LOCKED` queues, `GROUPING SETS`, row-level security, time ranges, change capture), and every SQL block in them deploys in CI. The skills belong to the project: edit them as your conventions change.

Secrets no longer need a file or the command line. Every `PGMI_PARAM_<NAME>` environment variable becomes parameter `<name>`, and `--params-file -` reads from stdin:

```bash
PGMI_PARAM_DB_PASSWORD="$DB_PASSWORD" pgmi deploy . -d myapp --param env=prod
```

A failed deploy says where it failed. `--json` names the file, `deploy.sql` included, and the line, a line inside a test included; `phase` tells a failing test from a failing schema change, and the human line says what the failure left behind: nothing committed, or a database created and left empty ([CLI](docs/CLI.md)).

A Go program can run a deploy itself. `pkg/pgmi/deploy` is the same entry point the CLI and `pgmi serve` use; it returns a `pgmi.DeployResult` and hands that deploy's PostgreSQL notices to your handler ([Go library](docs/GO-LIBRARY.md)). The library API is pre-1.0.

Pick the starting point that fits. `pgmi init` alone writes the smallest project: a `deploy.sql` loop, one SQL file and a `.pgmiignore`. `--template basic` adds two layers, run-once tables and re-run views and functions, with tests. `--template advanced` is the application stack below ([templates](docs/templates.md)).

**The advanced template.** One handler registry serves REST, JSON-RPC and MCP 2026-07-28 and publishes a live OpenAPI 3.1 document. Identity comes from sign-in sessions and organization sessions, with permissions from a catalogue in `data/authorization.json`. Row-level security is forced on every table, and the deploy refuses a foreign key that could cross from one organization's rows to another's. Background jobs, change tracking and an outbox come with it ([advanced template](docs/advanced/README.md)).

An MCP client no longer acts as the person who connected it. It acts *for* them, as an agent with its own principal and one of three agent roles (`agent.reader`, `agent.contributor`, `agent.delegate`), and it can never do more than its person. The audit log, change tracking and the exchange log record both, so an agent's change is never mistaken for its person's. The client signs in with OAuth at your identity provider; its first call answers a short pairing code, and the person approves it on the web:

```bash
curl -X POST https://app.example.com/api/connections/approve \
  -H "Content-Type: application/json" -H "X-CSRF-Token: $CSRF" --cookie "__Host-session=$SESSION" \
  -d '{"pairingCode": "K7M2QX", "organizationId": "'"$ORG"'", "roleId": "agent.reader"}'
```

People pause, resume and revoke their agents, managers do the same for their organization, and the owner can switch every agent off.

Reference gateways in Python, TypeScript, Go, C# and Rust serve all of it: REST, MCP, OIDC sign-in, the OAuth resource server and rate limits. Each is written the way its language is usually written, for people and coding agents to read and port. They follow one written protocol and pass one end-to-end suite in CI. Each has two extension seams: a database channel for retries, tracing or caching, and interceptors that see the caller's headers and change a request before its transaction or a response after its commit, such as adding an embedding or signing a storage URL ([gateway protocol](shared/templates/advanced/gateways/PROTOCOL.md)).

### Upgrading

**pgmi itself and the basic template**

- **PostgreSQL 15 or newer.** The advanced template needs 18. Building pgmi from source needs Go 1.27; releases are built with Go 1.27.3.
- **`pgmi init` without `--template` writes the minimal project** (`deploy.sql`, `hello_world.sql` and `.pgmiignore`), not the basic template. A script that relied on the old default must pass `--template basic` ([templates](docs/CLI.md#templates)).
- **`pgmi ai setup`, `check`, `skill`, `skills` and `client` are removed.** Skills ship in the scaffold instead: `pgmi init` writes `AGENTS.md`, `CLAUDE.md` and `.agents/skills/`. To add them to an existing project, scaffold a new one and copy those three. `pgmi ai` keeps the overview and `pgmi ai contract`.
- **Session API: `pgmi_plan_view.checksum` is now `pgmi_checksum`.** Rename it in your deploy.sql. The contract version stays `1`: before 1.0, `--compat 1` does not catch a renamed column, so pin the pgmi binary. Every public view now has an explicit column list, and the new `pgmi_runtime_view` names the pgmi and contract version running the deploy ([core versus template](docs/core-vs-template.md)).
- **Session API: file paths sort in byte order.** `path`, `name` and `directory` in `pgmi_source_view`, and the path columns of the test views, are now `COLLATE "C"`, so `ORDER BY path` runs files in the same order on every server; it used to follow the database's locale (`./ab.sql` before `./A.sql` under `en_US.utf8`). A deploy.sql that ordered by path on such a server may now run its files in a different order ([session API](docs/session-api.md)).
- **`pgmi_checksum` keeps whitespace inside string literals, quoted identifiers and dollar-quoted bodies**, as it already kept their case: `'a  b'` and `'a b'` are different data. The normalized checksum changes once for a file with a whitespace run inside one, so a run-once file of that kind already applied is refused once as changed ([session API](docs/session-api.md)).
- **A connection string without a database never deploys into `postgres`.** The target comes from `-d`, then the string, then `PGDATABASE`, then `pgmi.yaml`; with none of them the deploy exits 10. It used to deploy into `postgres` and exit 0 ([configuration](docs/CONFIGURATION.md)).
- **Connection strings.** Fields a `--connection` string leaves out now come from `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGAPPNAME` and `PGCONNECT_TIMEOUT`, as in libpq, instead of `localhost:5432/postgres`. Keywords libpq does not know are refused instead of being passed to the server. `service=` and `PGSERVICE` are refused. ADO.NET strings read as Npgsql reads them: a single `Host=h:port` connects, `SSL Mode` takes Npgsql's names, and a client option such as `Pooling` is refused ([connections](docs/CONNECTIONS.md)).
- **Environment variables that replace `pgmi.yaml` values are named.** Each `PG*` variable, TLS setting or connection-string field that replaces a `pgmi.yaml` value prints one line on stderr, such as `sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)`, and `--json` records it under `overrides`. A connection string never takes `sslmode` from `pgmi.yaml`, and pgmi says so. A set `PGOPTIONS` and the keys the project `.env` applied are named too, never their values ([configuration](docs/CONFIGURATION.md)).
- **`-h`, `-p` or `-U` next to `PGMI_CONNECTION_STRING` or `DATABASE_URL` exits 10.** It used to drop the variable silently and connect to localhost as the OS user. `--sslmode` now applies on top of the variable instead of dropping it, and the replaced value is announced.
- **A `--azure`, `--aws` or `--google` flag overrides `pgmi.yaml`'s `auth_method`** instead of losing to it, a detail flag such as `--aws-region` refines `pgmi.yaml`'s method, and an unknown `auth_method` exits 10 instead of falling back to a password. `--azure-client-id` without a secret now signs in as that managed identity, and `--azure-tenant-id` alone sets the tenant of the default chain ([configuration](docs/CONFIGURATION.md#auth_method), [Azure](docs/CONNECTIONS.md#azure-entra-id)).
- **Parameters.** `--param` keys that differ only in case are refused. `--params-file` reads a line as the project `.env` does, or exits 10 naming the line and never the value: `pw=secret  # rotated` no longer sends `secret  # rotated`, and a `$`, or a `\` in double quotes, must be single-quoted ([configuration](docs/CONFIGURATION.md#parameter-file-format)).
- **Exit codes.** More failures now exit with the code for their cause instead of 1:
  - **2:** `pgmi config` without a terminal.
  - **10:** a symlinked, unreadable or non-regular project file; a NUL byte or invalid UTF-8 in deploy.sql; an invalid `pgmi_test()` callback name; an invalid `sslmode`; a malformed `PGMI_MAX_FILE_SIZE`.
  - **11:** a cloud token that cannot be obtained.
  - **13:** a `pgmi_test()` pattern the server refuses; a server that refuses pgmi's session preparation; a deploy role without `CREATEDB` naming a missing database.

  A named pipe in the project used to hang the deploy past `--timeout` and Ctrl-C.
- **`--overwrite` waits for nobody.** It exits 15 while another deploy runs against the target, where it used to end that deploy, and two `--overwrite` runs of one database can no longer destroy each other. It refuses before the drop what it cannot restore, and the recreated database keeps its locale provider, its `search_path` and other list settings, and its per-role settings ([the overwrite lifecycle](docs/CONNECTIONS.md#the---overwrite-lifecycle)).
- **`pgmi serve` no longer negotiates MCP 2025-03-26**, whose JSON-RPC batches pgmi never implemented. It speaks 2026-07-28, 2025-11-25, 2025-06-18 and 2024-11-05.
- **Go library.** `pkg/pgmi` exports only what `deploy.New` takes and returns: `pgmi.Deployer`, `ConnectionConfig`, `NewScriptError`, `ValidateDunderDirectories`, `IsTemplateDatabase`, `IsSQLExtension`, `IsTestPath`, `PgmiignoreFile`, `MinimumServerVersionNum` and the `Default*` constants are no longer public. `DeploymentConfig.Timeout` is removed, since the library never read it: bound a deploy with the context you pass to `Deploy` ([Go library](docs/GO-LIBRARY.md)).
- **The basic template is new.** `physical/` holds run-once table files, tracked in `deployment.run_once`; a deploy refuses one that changed after it ran, and one without `<pgmi-meta idempotent="false">`, and, in both templates, a run-once file with more than one sort key. `semantic/` holds views and functions, re-run on every deploy. An SQL file anywhere else, outside `__test__/`, is refused before anything runs. `env` is required (`dev`, `test` or `prod`) and the scaffolded `pgmi.yaml` no longer sets it, so a CI deploy that forgets it fails with SQLSTATE 22023 before any change instead of seeding demo data: pass `--param env=dev` locally and `PGMI_PARAM_ENV` in CI. Only `dev` seeds the demo project. Existing basic projects keep working; the new layout is for new projects ([basic template](docs/basic/README.md)).
- **`--verbose` is quieter.** It no longer floods the console with server DEBUG lines from pgmi's own setup; `RAISE DEBUG` from your deploy.sql and tests still shows.

**The advanced template**

- **A new database.** See [the upgrade guide](docs/advanced/UPGRADING.md#from-v013-to-v014-a-new-database), which also translates the old handler declarations. `membership/` cannot be removed yet: the deploy fails at the first `lib/` file without it.
- **Identity.** `requiresAuth` is replaced by `access` (`public`, `authenticated`, `organization`) or `permissions`; `organization` admits any signed-in member in an organization session. API keys are removed. A request's identity comes from its login session and organization session, never from a header naming a user.
- **MCP clients.** The gateway ignores `X-User-Id` and `X-Tenant-Id`. A person approves each client on the web, naming `organizationId` and an agent `roleId`, and the client then sends the handle as `Authorization: Bearer cn_...` or `X-Connection`, or signs in with OAuth (`MCP_RESOURCE`, `MCP_AUTHORIZATION_SERVERS`). With OAuth on, an anonymous `/mcp` call is 401; an unknown, revoked or expired handle is 401 either way. If you add permissions to `data/authorization.json`, add them to the agent roles that should hold them.
- **Routes and registration.** Path templates are the only route declaration (`/projects/{id:uuid}`), route versioning is removed, and `querySchema` declares and enforces query parameters. Registration refuses an unknown metadata key and names the near match (`readonly`: did you mean `readOnly`?), so a project with a mistyped key now fails its deploy. A 201 route declares `locationTemplate`, or `noLocation` with the reason, and `successStatus` lists every 2xx the body can answer ([metadata keys](shared/templates/advanced/.agents/skills/pgmi-endpoint/references/metadata.md)).
- **Declared schemas are enforced.** An `inputSchema` holds its types, required fields, bounds, sizes and `const` at every depth, for REST bodies, RPC params and MCP tool arguments; a schema using a keyword the database does not enforce (`oneOf`, `if`, `multipleOf`, ...) is refused at registration, so a published schema is an enforced one ([MCP handlers](docs/advanced/MCP-HANDLERS.md)).
- **Errors.** REST, JSON-RPC and MCP share one error contract. Every problem answer carries a namespaced `code`: `pgmi.*` for the framework, `<schema>.<entity>.<reason>` for the application, such as `membership.connection.role_too_broad`, and `pgmi.login.*` for gateway sign-in. A client matching on the old unprefixed codes must change. A field the caller can fix is 422 with `invalid-params` naming it. An error answer rolls back the handler's writes; its exchange row still commits.
- **MCP 2026-07-28** beside the legacy `initialize` handshake. Tools declare their effect, and a tool's result is checked against its `outputSchema`. A missing resource answers `-32602`. pgmi's MCP error codes leave the JSON-RPC reserved range: no permission or no organization is `-31003` (was `-32003`), retries exhausted and no database connection `-31000` (was `-32000`), a client that cannot act yet `-31001`, and a transaction below the isolation floor `-32603` (was `-32600`). Branch on `data.code`. The MCP header checks (`MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name`) run in the database, so a gateway you ported must forward those headers ([MCP protocol](docs/advanced/MCP-PROTOCOL.md)).
- **Gateway.** `tools/mcp-gateway.py` is now `gateways/python/gateway.py`, beside gateways in four other languages; the old `clients/` folder is `gateways/`. Settings are renamed and shared by every gateway: `MCP_ALLOWED_ORIGINS` is `ALLOWED_ORIGINS`, `MCP_MAX_RETRY_ATTEMPTS` is `RETRY_MAX_ATTEMPTS`, `MCP_RETRY_BASE_DELAY` is `RETRY_BASE_DELAY`, and `MCP_RETRY_MAX_DELAY` and `MCP_RETRY_AFTER` are gone. The gateway now also serves REST under `/api`; the deploy parameter `api_base_path` (default `/api`) names that mount in the OpenAPI document. Keep the gateway's `DATABASE_URL` as secret as a token-signing key: that login asserts who is signed in.
- **Exchange log.** The exchange views and `GET /admin/exchanges` drop the pending fields, which could never be true (`is_pending`, `pending_count`, `oldest_pending_at`, `isPending`, `pendingCount`, `oldestPendingAt`). `statusCode` and `summary` are nullable, and the log is indexed by id, request id, time and acting principal.
- **Parameter refusals are SQLSTATE 22023**, so CI tells a missing secret from a failed migration, and they tell you to pass a missing password as `PGMI_PARAM_<NAME>` or through `--params-file -`, never `--param`.

### Also in this release

**pgmi**

- **`.pgmiignore`** keeps files out of the deploy session with gitignore patterns ([what gets loaded](docs/DEPLOY-GUIDE.md#what-gets-loaded)). `--verbose` and `--json` list every ignored path and the rule that ignored it. The `.pgmiignore` of all three templates, and the basic and advanced `.gitignore`, keep out parameter files (`*.params`, `*.env`) and editor, Windows and macOS copies such as `x (1).sql` and `x - Copy.sql`, which would otherwise sort and run before the original.
- **`--json` carries provenance:** the pgmi, contract and server versions, the target, the start and finish times, `connectionSource`, `overrides`, `dotenvKeys`, the deploy's notices with their DETAIL and HINT, and `tls` (whether the session was encrypted, the TLS version and the effective `sslmode`). The human summary and the override lines still reach stderr, so a CI log shows a recreated empty database either way ([CLI reference](docs/CLI.md), [production](docs/PRODUCTION.md#connection-requirements)).
- **Failure locations hold in harder cases.** A comment header, a function body or a leading `DO` block no longer hides the failing line, a line past the end of the file is never reported, an editor backup with the same text is never blamed, and a deploy stopped by `--timeout` or Ctrl-C is cancelled on the server, so the error names the file that was running.
- **`COMMIT AND CHAIN` counts as a commit**, so `unitsCommitted` and `executionMode` are right for a deploy.sql that ends its head with it, and a `$` inside an unquoted identifier such as `foo$bar$` no longer opens a dollar quote and swallows the `COMMIT` after it ([the execution contract](docs/DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract)).
- **Checks run before anything is touched.** The server version is checked before `--overwrite` or create-if-missing drops or creates a database, a mistyped project path is reported as a missing path (exit 10) before anything asks for a database name, and a missing maintenance database says so instead of suggesting `--overwrite`. `pgmi.yaml`'s `maintenance_database` now holds without `-d`.
- **pgmi's own suggestions run as printed.** The run-once drift hints name the failing database with `-d`, shell-quoted when it needs it, and leave out `--force`; they and `pgmi init`'s next step pass `--param env=dev`. Errors read as plain words, name the database a connection failed on, and name the `--timeout` that ran out with how to lift it. On a stderr that is no terminal pgmi prints no glyphs or colours.
- **`pgmi serve`** speaks MCP 2026-07-28, resolves its connection exactly like `pgmi deploy`, and ignores `.env` (pass parameters or set them in the server's environment). A deploy answers one line with its notices, plus the exit code and phase on failure, so a text-only MCP client sees what happened; unknown tool arguments are refused by name, every tool marks whether it is read-only, and deploys to different databases run at the same time.
- **CLI and wizards.** No command stalls 5 s querying the terminal. Cancelling `init`, `config` or the deploy wizard writes nothing and exits 130 (Ctrl-C) or 12. A failed connection test keeps the form and names the SQLSTATE. Saving a connection edits only `pgmi.yaml`'s connection block, keeps its comments and keeps its `sslmode` and certificate paths. `.pgpass` is saved for the maintenance database too. `pgmi init` works in a directory that holds only `.git`. Wizards fit 80x24. `--template` and `--sslmode` complete in the shell.
- **`pgmi metadata`.** `scaffold` keeps path order, escapes keys and descriptions as XML and keeps a BOM first; `plan` prints a table; `validate` reports every bad `<pgmi-meta>` block at once; an unterminated `/*` comment no longer crashes the scan.
- **Go library.** `deploy.WithNoticeHandler` receives one deploy's notices only, so concurrent deploys never mix them, and nothing in the library writes to stderr. `Session.Close` no longer waits 15 s on a connection broken by a timeout.
- **Connection strings reach the server as written.** A space in `options` or `application_name` is no longer turned into `+`, a `?dbname=` names the target instead of overriding it, and a password without a user is kept.
- **Documentation.** One page compares the minimal, basic and advanced templates; the basic and advanced templates each have a story-first entry page and new diagrams of their layers and tables; one page lists every environment variable pgmi, the driver and the cloud SDKs read, with complete GitHub Actions and GitLab jobs ([environment reference](docs/CICD.md#environment-reference)); and one section says what pgmi guarantees in an unattended run ([Running unattended](docs/PRODUCTION.md#running-unattended)). Azure sign-in is tested by hand against a real Flexible Server; AWS and Google sign-in are unit-tested only, and the docs say so.
- **Dependencies.** Built with Go 1.27.3; pgx 5.12.0, Bubble Tea 2.1.1, and current Azure, AWS and Cloud SQL SDKs.
- **The Cloudsmith APT repository is gone.** Nobody installed pgmi from it. Debian and Ubuntu users install the `.deb` package attached to each [GitHub release](https://github.com/vvka-141/pgmi/releases) instead: `sudo apt install ./pgmi_<version>_linux_amd64.deb`. If you added the Cloudsmith source, remove it from `/etc/apt/sources.list.d/`.

**The advanced template**

- **The deploy trusts no role it did not create.** An existing admin, owner, kernel, api or jobs role that another login holds ADMIN on, or that has a member the deploy did not grant, is refused (22023), so a login that may create roles cannot pre-create them and take over the new database. The jobs login gets its jobs only once it is registered as a service principal. A role name longer than PostgreSQL allows is refused before any role is created. The template deploys as a plain `CREATEROLE` + `CREATEDB` role, to a database whose name needs quoting, and with a custom owner role ([templates](docs/templates.md), [the jobs login](shared/templates/advanced/README.md#the-jobs-login-optional)).
- **Tenant isolation.** A signed-in person without an organization session reaches no organization's data, through a kernel either; a database relabelled from dev loses dev sign-in; an unverified login holding an operator's email is not made platform operator and never reserves its address; a catalog sync never cascades into rows its check could not see; the gateway's login can no longer write the exchange log, and routes that return a credential never write it there. Two simultaneous first sign-ins no longer fail with a duplicate key, and requests on one session no longer queue behind each other.
- **HTTP behaves as clients expect.** The most specific `Accept` range decides (`application/json;q=0, */*` refuses JSON). URL mistakes such as invalid UTF-8 or an overflowing integer are 400s, not 500s. A handler that answers no status or one outside 100–599 is a 500 and keeps none of its writes. Lists report their total from the same snapshot as the page, and an oversized offset is a 400. `POST /invitations` answers 201 only when it creates the invitation, and 200 for a repeat. `POST /organization-sessions` answers 200, since it returns a handle rather than a resource, and `PUT /members/{id}/roles/{roleId}` answers 201 when the grant is new. `GET /admin/handlers` and `/admin/routes` answer the whole registry.
- **JSON-RPC is a first-class surface.** It enforces a handler's `inputSchema`, `accepts` and `produces` as REST does, refuses an invalid envelope with `-32600` before the method lookup, and refuses with `-32010` (409) and `-32011` (422) as a REST body refuses. The template ships one action, `organization.transfer_ownership`, and the Go gateway routes `POST /rpc` ([protocols](shared/templates/advanced/gateways/PROTOCOL.md)).
- **MCP.** `resources/read` routes a URI to the template with the most literal characters; every complete read carries `ttlMs` and `cacheScope`; tool errors count as errors in the exchange views, and an MCP exchange replays as its logged caller. A body PostgreSQL cannot store, such as one holding `\u0000`, is 400 `-32700` instead of a 500.
- **Jobs and the outbox.** A job run that fails no longer leaves the job locked, a skipped run says so, and `CALL api.job_run` inside a transaction block is refused without holding the lock. The `pgmi-handlers` skill ships a tested outbox that a job drains through a kernel under forced row-level security.
- **The reference gateways agree on the wire** ([gateway protocol](shared/templates/advanced/gateways/PROTOCOL.md)):
  - Every gateway serves `GET /health` and `POST /auth/logout`, answers an unknown path 404 `pgmi.not_found`, a body over 1 MiB 413, request headers over 64 KiB 431, a stalled body 408, and an unreachable database or an exhausted pool 503 `pgmi.unavailable` with `Retry-After`. No 1xx, 204 or 304 carries `Content-Length`.
  - Each transaction gets what is left of `REQUEST_DEADLINE_SECONDS` (default 30) as its `statement_timeout`, a slow handler is answered 504 `pgmi.deadline_exceeded`, and a client that hangs up cancels its query.
  - Tokens are accepted when signed with RS256, PS256 or ES256, and every other algorithm, EdDSA included, is refused; `exp`, `iat` and `nbf` must be numbers, and a present `client_id` wins over `azp`. The Python gateway verifies a PS256 key without `alg`, as Microsoft Entra ID publishes them, and refetches signing keys at once after a rotation.
  - The session and CSRF cookies are `SameSite=Strict`; `/auth` is rate-limited per address; the published OpenAPI document describes the session cookie and `X-CSRF-Token`; a sign-in without `returnTo` lands on the API explorer at `/api/docs`.
  - `sslmode` is read as libpq reads it in every gateway: `verify-ca` or `verify-full` without `sslrootcert`, or an unknown mode, stops the gateway at startup, and Rust trusts only `sslrootcert`. A malformed `DATABASE_URL`, or one with an unencoded `@` in its password, stops the gateway with a line that never prints the password. Tracing lines log the path, never the query string.
  - The Python gateway never writes a header holding CR or LF and refuses a chunked body 411; the Rust gateway survives a failed accept; the TypeScript gateway refuses a `DATABASE_URL` parameter node-postgres would ignore, such as `require_auth`.
- **Agents manage their connections.** A repeated revoke answers 200, pausing a revoked connection 409, a member cannot undo a manager's pause by revoking or reconnecting, and `GET /admin/exchanges` filters by the acting principal.
- **Quieter, tidier deploys.** A converged redeploy prints no "already exists, skipping"; permission-guarded MCP tools deploy; a `readOnly` route is unlogged by default (`autoLog` defaults to `false` there, [transaction policy](docs/advanced/TRANSACTION-POLICY.md)); a handler body that calls a `pg_temp` function, which ends with the deploy session, fails the deploy's tests; a new project never contains gateway build output; `echo_note` ships as an MCP example.
- **Documentation and skills.** The Quick Start starts a gateway and calls `/api/hello`; the advanced entry page and `ARCHITECTURE.md` describe the template as shipped, with diagrams of its three layers, its main tables and a request's path; the shipped skills and `AGENTS.md` agree with the code and with each other, and route audit, API tests and job queues to the template's own mechanisms first.

## v0.13.1 — 2026-10-31

**If your connection string lists several hosts, pgmi now connects to the port you asked for.** Before this release it dialed 5432 on every listed host, so a deploy could land on a different PostgreSQL cluster. This release also updates dependencies that had known vulnerabilities.

### Upgrading

No action needed. One connection string that used to be accepted is now refused: `hostaddr` exits with code 10 (see below). Session API contract unchanged (v1).

### Fixed

- **A connection string listing several hosts connected on port 5432, whatever port you gave.** `postgresql://…@db1,db2/app?port=6000`, `host=db1,db2 port=6000`, `PGHOST=db1,db2 PGPORT=6000` and `-h db1,db2 -p 6000` all dialed 5432 on every host, so a deploy could reach a different cluster listening there. Each host now gets its own port or the one you set, as in libpq; a host with no port in a list that names others gets the default port. IPv6 and socket-directory members of a list now work too.
- **`hostaddr` is rejected** with exit 10. pgx does not support it, and pgmi treated it as `host`, so the TLS certificate was checked against the IP instead of the name.

### Security

- **`golang.org/x/net` updated to v0.60.0**, fixing five vulnerabilities that govulncheck reported as reachable from pgmi (GO-2026-6603, GO-2026-6610, GO-2026-6611, GO-2026-6612, GO-2026-6617). The Azure, Google Cloud SQL and other dependencies are updated too.

## v0.13.0 — 2026-09-25

**The advanced template's API now enforces the contract it publishes, and an advanced-template deploy can no longer fall back to the password `postgres` without being told to.** If you scaffolded the advanced template, read Upgrading: several changes stop a deploy that used to pass, on purpose.

The advanced template publishes an OpenAPI document for every route it serves. Until now that document promised more than the gateway checked: request bodies, query parameters and authentication schemes were described but not all enforced, and a route could be reachable at an address two routes both claimed. Every declared element is now checked by the gateway before your handler runs, and a new test in the template fails the deploy if any declaration goes unenforced, for routes you add too.

### What you can do now

Declare a query parameter once, and pgmi enforces it and publishes it:

```sql
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'e1000001-0001-4000-8000-000000000001',
        'path', '/reports',
        'httpMethod', '^GET$',
        'name', 'list_reports',
        'requiresAuth', false,
        'outputSchema', jsonb_build_object('type', 'object'),
        'query', jsonb_build_array(
            jsonb_build_object('name', 'format', 'required', true,
                'schema', jsonb_build_object('type', 'string', 'enum', jsonb_build_array('json', 'csv'))))),
    $body$ BEGIN RETURN api.json_response(200, '{}'::jsonb); END; $body$);
```

`GET /reports?format=pdf` now answers `400` naming the parameter before your handler runs, and `/openapi.json` lists `format` as a required `in: query` parameter. Request bodies get the same treatment against the route's `inputSchema`.

Connection strings in the form psql uses also work now:

```bash
PGMI_CONNECTION_STRING="host=db1 port=5432 dbname=app user=deploy" pgmi deploy . -d app
```

### Upgrading

- **Advanced-template projects** — the template is your code, so merge the changes into your project three ways, against the template you started from: [Upgrading an advanced-template project](docs/advanced/UPGRADING.md).

  Then, before redeploying:
  - **Pass `env` on every deploy.** A missing `env` used to mean `dev`, and `dev` fills missing passwords with `postgres`. With `env: dev` committed in `pgmi.yaml`, a production deploy whose params file forgot the admin password created an admin login with password `postgres` and exited 0. `env` now has no default. Use `--param env=dev` locally, and put `env` and `database_admin_password` in a params file everywhere else. Remove `env: dev` from your committed `pgmi.yaml`.
  - **Every SQL file needs a `<pgmi-meta>` header with a sort key.** A file without one ran before the framework and failed with an unrelated error such as `type "core.entity_id" does not exist`. The deploy now stops and names it. Give your files a sort key of `005/...` or later.
  - **Request bodies and query values are checked before your handler.** A `POST`, `PUT` or `PATCH` body that does not match the route's `inputSchema`, or a query value of the wrong type, now gets `400` from the gateway. If a handler relied on receiving such a request, relax the declaration.
  - **Two routes that can claim one address no longer both register**, for example `/shops/{shop}/orders` beside `/shops/main/{section}`. Make one of them more specific.
  - **`/openapi.json` shows each caller only the routes it may call**, so fetch it with the identity header when you generate a client. Its `ETag` values changed format.
  - **MCP callers without identity** get `-32602` "not found … (or it requires authentication)" for a protected tool, resource or prompt, instead of `-32001`.
- **`pgmi_parameter_view`** now has only `key` and `value`. The `type`, `required`, `default_value` and `description` columns were always empty. If your `deploy.sql` selects them, drop them. Parameter checks belong in your SQL, as the advanced template's `session.xml` shows.
- **`pgmi metadata plan`** prints to stdout, uses camelCase keys, lists the same rows as `pgmi_plan_view`, and exits 10 with a JSON error on invalid metadata.
- **`pgmi serve`** speaks MCP 2025-11-25, and still negotiates down for older clients.
- **Everything else:** no action needed. The session API contract is still v1; the parameter view change above is the only change to it.

### Also in this release

- **Redeploys no longer block your application.** An unchanged redeploy of the advanced template used to take exclusive locks on the tables and views your API reads, so every request waited for the whole deploy. It now changes a policy, column or view only when it differs, and waits at most 5 seconds for any lock it still needs.
- **Failures name the file and line** that failed, taken from the text PostgreSQL was running, instead of the line inside `deploy.sql` that ran it.
- **Problems are caught before pgmi connects**: an unreadable `deploy.sql`, an invalid parameter name, two parameters differing only in case, or a project file that is binary or over 10 MiB now exit 10 without touching the server.
- **`pgmi deploy --json` reports `"created"`**, true when the deploy created the target database, which on a routine deploy usually means a mistyped `-d`. The security guide explains why production deploy roles should not have `CREATEDB`.
- **Connection strings**: IPv6 and Unix-socket hosts, and URIs listing several hosts such as a primary and a standby, no longer break when pgmi rebuilds the string. A connection string in `PGMI_CONNECTION_STRING` or `DATABASE_URL` no longer blocks `-h`, `-p` or `-U`, and pgmi says which source it used.
- **Connection retries** now say so on stderr instead of stalling silently, and only failures to connect are retried.
- **A green advanced-template deploy says what happened in about 130 lines**, not 665.
- **`pgmi serve`** answers ping while a deploy runs, stops a deploy when the client cancels it, and returns the deploy's warnings.
- **`pgmi init` records the pgmi version and template** as the first line of `deploy.sql`, and `pgmi info` shows it, so after a template fix you can tell whether your project predates it.
- **`--overwrite` keeps the database's grants** as well as its encoding and settings.
- On Windows, with `APPDATA` unset, the password saved by the connection wizard could land in the working directory. It no longer does.
- **`/docs` loads a version-pinned API explorer** with an integrity hash, instead of whatever the CDN served that day, and `OPTIONS` preflight requests now succeed.
- **New example: apply-once tracking** — migrations that run once each, with a check that fails the deploy when an applied migration is edited. It runs in CI with the other examples.
- **Release artifacts carry build provenance.** Check an archive with `gh attestation verify <archive> -R vvka-141/pgmi`.
- Many documentation corrections, including claims about `--compat`, idempotence and checksums that did not match the code.

## v0.12.1 — 2026-09-24

**A security release for the advanced template: three ways one user could end up signed in as another are closed, and credentials no longer land in the request logs.** If you scaffolded the advanced template, read Upgrading — the template is your code, so a new pgmi binary changes nothing until you take the fixes into your project.

A full review of the release candidate found these, each reproduced against a live deployment before it was fixed:

- **A second sign-in with a matching email took over the account.** A new identity whose email already belonged to an account was linked to it, verified or not. An identity provider that lets users set their own email, or simply a second provider, signed its user in as the account's owner, administrators included. Linking now requires the email to be verified on both sides; otherwise the new identity stays unlinked and gets `401`.
- **Users of one enterprise connection became one user.** Subjects shaped like `samlp|<connection>|<user>` (Auth0 enterprise and custom OAuth connections) were cut at the second `|`, so everyone on that connection resolved to the same account. The subject is now everything after the first `|`.
- **The customer role could claim to be anyone.** It was a login role, and row-level security identifies the caller from a session setting any session can set. Anyone holding its password could act as any user and mint working API keys for them. The customer role is now `NOLOGIN`; only the gateway acts for a user.
- **Credentials were written to the exchange logs.** Every logged request kept its `Authorization`, `Cookie` and API-key headers in plaintext for the retention period, and the admin replay endpoint returned them. Logged copies now drop them.

It also fixes the API keys v0.12.0 stopped accepting: any key id other than exactly 12 lowercase hex characters — keys seeded by hand, or issued by v0.10.0 — was refused as `malformed key`.

### Upgrading

- **Advanced-template projects:** merge the changes into your project three ways, against the template you started from: [Upgrading an advanced-template project](docs/advanced/UPGRADING.md).

  Then, before redeploying:
  - Remove `database_customer_password` from your parameters or params file: the parameter no longer exists and the deploy rejects unknown parameters. Anything that logs in as the customer role directly must go through the gateway; the deploy turns the role's login off.
  - Users who signed in through a second identity provider with an unverified email were linked to the matching account. Review `membership.user_identity` for identities you did not intend, and remove them.
  - If your identity provider issues subjects with more than one `|`, users of the same connection were merged into one account; their `membership.user_identity.idp_subject_id` holds only the connection name. That cannot be split automatically: set it to the full `<connection>|<user>` of the account's real owner, and the other users of that connection sign in as new accounts.
  - Credentials already in the exchange logs stay there until you scrub them. After redeploying:

    ```sql
    UPDATE api.rest_exchange SET request = internal.loggable(request), response = internal.loggable(response);
    UPDATE api.rpc_exchange  SET request = internal.loggable(request), response = internal.loggable(response);
    ```
- **deploy.sql that ends inside an open transaction** — a `BEGIN` without its final `COMMIT` — now fails with exit 13. It used to exit 0 while nothing from that transaction was applied.
- **Building from source** (`go install`) now needs Go 1.26: the `golang.org/x/crypto` security fix requires it. With Go's default toolchain setting an older Go downloads 1.26 automatically. Prebuilt binaries and packages are unaffected.
- **Everything else:** no action needed. The session API contract is unchanged at v1.

### Also in this release

- Dependency security updates for four advisories reachable from pgmi's code: GO-2026-6355 and GO-2026-6354 (`golang.org/x/crypto` 0.56.0), GO-2026-6348 (`google.golang.org/grpc` 1.83.1) and GO-2026-6253 (`github.com/moby/go-archive` 0.3.0).
- `pgmi deploy --aws --aws-region …` and `--google --google-instance …` always failed with "requires region/instance", whatever was passed. They now reach the cloud provider.
- A mistyped subcommand — `pgmi metadata validte .`, `pgmi ai chek` — printed help and exited 0, so a CI step with a typo passed having checked nothing. It now exits 2.
- The advanced template's endpoint recipe — a `GET` and a `POST` on one path — failed its own route test on every deploy. It deploys.
- The README and Quickstart Docker demo waited for nothing: pasted quickly, `pgmi deploy` reached PostgreSQL while it was still initialising and failed. Every Docker snippet now starts the server and waits for it in one block, then deploys in a second, so an error from `docker run` — a port already in use — stops you before a deploy can reach whatever server holds that port.
- A server that closes the connection — still starting up, restarting, or not PostgreSQL at all — was reported as an SSL/TLS problem and pointed you at `--sslmode`. It now says the server closed the connection and suggests `pg_isready`.
- `install.ps1` closed your PowerShell window when it failed, taking the error message with it, and on success left strict mode and its variables behind in your session. It now reports errors, leaves the shell open, and leaves nothing behind.
- `install.sh` needed `sudo` to write to `/usr/local/bin`, which fails in containers without it. It now tells you to rerun with `INSTALL_DIR="$HOME/.local/bin"`, and creates that directory. Both installers use `GITHUB_TOKEN` when set, so a rate-limited network can still resolve the latest version.
- `go install` builds printed `pgmi v0.12.0` where release binaries print `pgmi 0.12.0`. Both now print the same format.
- The lock-safe example's cleanup of a failed concurrent index build queued for `ACCESS EXCLUSIVE` for up to 3 seconds — rebuilding the lock queue the example exists to avoid — and could drop another session's in-progress build. It now waits at most 250ms for the lock and re-checks the index while holding it. If you copied that cleanup, update it.
- Several documented commands did not work as written: the CI guide pinned v0.11.0, the advanced README's handler example failed the template's tests, and the Quickstart's Docker detour left a connection string set that silently overrode `pgmi.yaml`. All fixed.
- Every release is now gated on the README and Quickstart walkthrough passing against a GoReleaser build of the tagged commit, and every install channel is checked after each release and nightly.

## v0.12.0 — 2026-07-30

**Your deployment script can now run `CREATE INDEX CONCURRENTLY`, and your API routes can declare their own transaction policy** — an isolation floor and a read-only flag that the gateway resolves and opens automatically, with a real retry contract for serialization failures. PostgREST can pin an isolation level per function; we know of nothing that lets a route *declare* read-only + `DEFERRABLE`, enforce the floor server-side even when the proxy misbehaves, publish the policy in OpenAPI, and apply it uniformly across REST, RPC, and MCP — with a documented retry contract for the serialization failures a strong level invites.

Two ideas carry this release. First, deploy.sql gains a one-sentence execution contract: *before your first top-level `COMMIT`, pgmi's atomic mode; after it, psql mode* — so zero-downtime phased deployments (schema change → concurrent index → transactional backfill → another index) read top to bottom and actually work. Second, the advanced template's routes become self-governing: declare `minTransactionIsolation` and `readOnly` in handler metadata, and callers just work — no header knowledge required, no way to run below the floor.

### What you can do now

A lock-safe, zero-downtime deployment as one readable file:

```sql
BEGIN;                                   -- atomic head: schema change + gated tests
ALTER TABLE orders ADD COLUMN status text NOT NULL DEFAULT 'pending';
CALL pgmi_test();
COMMIT;

CREATE INDEX CONCURRENTLY idx_orders_status ON orders (status);   -- psql mode

BEGIN;                                   -- an atomic backfill says so explicitly
UPDATE orders SET status = 'archived' WHERE created_at < now() - interval '10 years';
COMMIT;
```

Runnable end to end in `examples/lock-safe-deploy/`, asserted in CI.

And a route that governs its own transaction:

```sql
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'uri', '^/report$', 'httpMethod', '^GET$', 'name', 'report',
        'minTransactionIsolation', 'serializable',
        'readOnly', true, ...),
    $body$ ... $body$);
```

The shipped MCP gateway implements this end to end: it resolves the policy
before `BEGIN` and opens `SERIALIZABLE READ ONLY DEFERRABLE` — which can
never abort with a serialization failure, so the route needs no retry logic
at all. Routes that *can* hit `40001` propagate it with the SQLSTATE intact,
and the gateway retries a fresh transaction with backoff. For REST/RPC, the
template publishes `api.rest_route_policy` / `api.rpc_route_policy` for the
reverse proxy you supply to call — and the database refuses dispatch below
the declared policy either way. OpenAPI advertises every route's policy,
including an honest `x-pgmi-replica-safe` hint.

### Upgrading

- **The session API contract is unchanged at v1** — existing `deploy.sql`
  files keep working. Plan ordering is now locale-proof (`COLLATE "C"`), which
  can reorder plans that previously depended on the server's locale; projects
  using explicit sortKeys are unaffected.
- **Execution contract**: statements after your first top-level `COMMIT` now
  run per-statement (psql mode) instead of inside one implicit block. Scripts
  with no mid-file top-level `COMMIT` are byte-for-byte unaffected. The
  scaffolded templates _do_ have one — their DONE banner runs in the tail,
  which is a no-op; anything you append below that `COMMIT` autocommits. If a
  later phase must be atomic, write its own `BEGIN ... COMMIT`.
- **Advanced template vocabulary**: `requiredTransactionIsolation` is now
  `minTransactionIsolation` (the semantics were always a floor; the name now
  says so), and the OpenAPI extension is `x-pgmi-min-transaction-isolation`.
  Databases deployed from earlier templates migrate automatically on the next
  deploy.
- **`-h` is now `--host`, not `--help`**, matching psql and the rest of pgmi's
  connection flags (`-p`, `-U`, `-d`). Use `--help` for help. This closes a
  trap where `pgmi deploy . -h 127.0.0.1 -d app` printed help and exited 0
  having deployed nothing.
- **Hidden files and directories are no longer loaded** into the session,
  along with `node_modules/` and `__pycache__/`. If your project deliberately
  kept SQL or data under a dot-directory, move it; everything else is
  unaffected. `__test__`/`__tests__` are untouched.
- **The advanced template's customer role no longer inherits the api role.**
  Redeploying revokes that membership and restricts handler registration to
  the admin role. If your application logged in as the customer role and
  called `api.rest_invoke` directly, point it at the api role instead — that
  path was a privilege escalation to database owner.
- **A wrong HTTP method on an existing route now returns 405, not 404**, with
  the mandatory `Allow` header. `HEAD` is served wherever `GET` is. If you
  asserted 404 for a method mismatch, update the expectation.
- **Deactivation now takes effect immediately.** A deactivated user, a
  deactivated organization, and a disabled or expired API key all stop
  resolving an identity on the next request. An API key also reaches only the
  organization it was issued for. If anything relied on a key spanning a
  user's organizations, issue one key per organization.
- **`api.rest_route_policy`'s `p_version` must match the request.** Prefer the
  new `api.rest_route_policy_for(method, url, headers)`, which takes the same
  headers you pass to `rest_invoke` and therefore cannot resolve a different
  route than dispatch picks.
- **Route precedence is refreshed on every registration.** Previously
  `sequence_number` was assigned once and never updated, so a fresh database
  and an existing one could route differently from identical sources.
- **`pgmi serve`'s `ai_skills` tool now returns `name` / `description` /
  `filePath`**, the field names it has always advertised in its output schema;
  it was emitting `Name` and `Description`. Anything reading those keys from
  the MCP result needs the lowercase spelling.
- **`pgmi serve` requires a `protocolVersion` in the handshake.** It used to
  accept `initialize` with no params at all, while the advanced template's
  `api.mcp_initialize` rejected the same request — one product, two answers.
  Both now return `-32602`. A JSON-RPC `id` that is not a string or number,
  `null` included, returns `-32600` instead of being silently swallowed.
  Conforming clients send both and are unaffected.

### Also in this release

#### Running a deploy

- **A `BEGIN ATOMIC` function body no longer breaks the atomicity contract.**
  A SQL-standard function body is not dollar-quoted, so its `END` read as a
  `COMMIT`: a deploy.sql defining one and no explicit `COMMIT` — the shape the
  guide calls entirely atomic — silently committed everything up to that
  function, and a later failure left it applied. After a real `COMMIT` the same
  body was cut into invalid fragments instead. Such a body is now stepped over
  whole.
- **`pgmi deploy .` works inside a git repository.** File discovery walked
  `.git/`, and `.git/index` is binary, so the most-documented invocation failed
  in essentially every real project — and created the target database before
  failing. Projects are scanned before any database is created, so a project
  that cannot be scanned leaves nothing behind.
- **`--overwrite` recreates the database you had.** It dropped and recreated
  with the server defaults, so a LATIN1 or `C`-collation database came back as
  UTF8/`en_US.utf8` — encoding changes how bytes are stored, collation changes
  index ordering and every comparison. Owner, connection limit, per-database
  `ALTER DATABASE ... SET` values and the comment were discarded too; a
  `statement_timeout` set as a safeguard vanished silently.
- **Redeploying no longer destroys user objects.** `lib/common/encoding.sql`
  ran `DROP TYPE ... CASCADE` on every deploy, and CASCADE cannot tell the
  template's own casts from a column, view or function you typed `common.utf8`.
  The casts are dropped by name.
- **The deploy lock is released when it should be.** It was unlocked inside an
  aborted transaction — where it silently did nothing — so a failed tail left
  the lock alive until the server processed the disconnect. Release is now
  synchronous on session close, including when session preparation itself
  fails, so back-to-back deploys no longer race the previous session and an
  immediate retry is not greeted with exit 15.
- **A deploy to an existing database no longer touches a maintenance
  database**, so a CI role granted `CONNECT` on only its own database works.
- **A file saved by a Windows editor deploys.** A UTF-8 BOM is valid UTF-8, so
  it passed every check and reached PostgreSQL glued to the first keyword:
  `syntax error at or near "CREATE"` on a line that reads as correct, with
  nothing to point at the cause. Only `deploy.sql` was being stripped.
- **Two scripts that deploy differently no longer share one checksum.** The
  normalized checksum — the content identity behind idempotency tracking —
  lowercased the whole file, including string literals and quoted identifiers.
  `"Users"` and `"users"` are different tables and `'Production'` and
  `'production'` are different data, so an edit could read as no change and be
  skipped. Case is folded only where SQL itself ignores it.
- **Deploys got faster on projects with tests.** Autovacuum cannot see a temp
  table, so pgmi's session tables carried no statistics and every deploy.sql
  query was planned on default estimates. On a 20-directory project the test
  plan cost 546ms per call; it now costs 1.8ms, on every deploy.

#### The advanced template's web surface

- **A security-hardening pass.** The customer role could execute arbitrary SQL
  as the database owner — it inherited the gateway role, and PostgreSQL's
  default `PUBLIC` execute grant exposed the SECURITY DEFINER entrypoints and
  the handler-registration functions. The role graph is converged on every
  deploy and registration is admin-only. API-key lifecycle and creation are
  caller-authorized and tenant-scoped (a customer session could previously
  manage or mint keys across organizations); handler registration rejects name
  collisions instead of silently overwriting; error wrappers preserve the
  original SQLSTATE and DETAIL.
- **HTTP semantics.** Malformed client input returned 500, so 5xx alerting
  fired on user typos and retry middleware treated a permanently-bad request as
  retryable — the whole `22xxx` data-exception class now maps to 400/422. 401s
  carry the `WWW-Authenticate` challenge RFC 9110 makes mandatory, error
  responses no longer lose `Vary` and the catalog version, 304s stop
  contradicting the 200 they revalidate, and `Accept` honours `q=0` as the
  refusal it is — on the scaffolded Python gateway as well as the SQL one,
  which previously disagreed about the same header.
- **`autoLog: false` is honored when the handler fails.** It was applied on
  success and dropped on the error path, where the logged request carries
  headers and body — so a `POST /login` marked `autoLog: false` persisted
  plaintext credentials on any constraint violation.
- **Soft-deleted handlers stop serving traffic.** They were already absent from
  the OpenAPI document while all eleven gateway and discovery lookups still
  dispatched them: invisible in the contract, live in production.
- **Every shipped example route was a prefix catch-all.** A doubled backslash
  collapsed the query-string group to `.*`, so `/healthz` was served by the
  `/health` handler and `/hello/admin` by `/hello`. Registration rejects an
  unanchored pattern.
- **The MCP surface actually works over HTTP.** The scaffolded gateway wrote an
  MCP protocol version where the HTTP version belongs, so every response was
  unparseable by any client. It also refused connections until the host's
  reverse DNS answered, because `http.server` resolves the bind address before
  it listens — on a VPN or a container without a resolver it printed
  `Listening` and served nothing. The MCP dispatcher separately swallowed
  `40001`, committing lost writes while telling the caller "internal error";
  the retry contract now holds on MCP as on REST and RPC, with coverage on both.
- **The OpenAPI contract is cacheable and correct**: strong ETag with 304
  revalidation, a catalog version stamped on every response so cached route
  tables learn they are stale, and a fix for multi-parameter routes that
  produced invalid OpenAPI.
- **`pgmi serve` tool results conform to the schemas they advertise.** Six
  tools declared an output schema describing only success, while every failure
  returned a differently-shaped error object — which the MCP spec forbids and a
  validating client may reject rather than display. Each schema admits both
  shapes, so the failure envelope (SQLSTATE, exit code, notice stream) survives
  intact.

#### What pgmi tells you

- **The documented exit-code table now holds.** Configuration errors that
  landed on exit 1 or 13 return 10 — including a misspelled `__test__`
  directory — an unknown command returns 2, `--compat` is validated before
  `--overwrite` drops anything, and a deploy killed by `--timeout` exits 16
  rather than 13, so CI stops reporting a slow deploy as broken SQL.
- **A password saved to `.pgpass` is the one libpq uses.** The entry was
  appended, and libpq stops at the first line whose host, port, database and
  user match — so a common `*:*:*:user:oldpw` kept answering. pgmi printed
  "Saved" and the next connection authenticated with the stale password.
- **Failing SQL reports the file and line it came from**, instead of a
  position in a script you never wrote.

#### How pgmi itself is built and shipped

- **The release is better gated.** Tagging runs the five end-to-end example
  projects, refuses a tag that is not on `main`, refuses release notes still
  marked draft or undated, scans dependencies for known vulnerabilities, and
  builds every archive, package and checksum before publishing any of them.
  `make release-ready` runs the full suite rather than `-short`, and prints
  what it does not cover.
- **A vulnerability reachable from pgmi's own code is fixed.** Adding that scan
  immediately found one: `golang.org/x/text` before v0.39.0 could loop forever
  on invalid input, reached through `pgx.Connect` from the connection wizard
  (GO-2026-5970). Binaries from this release carry the fixed version.
- **The publishing pipeline installs a pinned CLI.** `cloudsmith-cli` was
  installed unpinned and then executed with the publication credential in its
  environment. It is pinned now, and the release token defaults to read-only
  with write granted solely to the job that publishes.
- **The MCP guidance shipped in the binary matches the code again.**
  `pgmi ai skill pgmi-mcp` documented `p_request_id` as `text` for five
  functions that take `jsonb`, listed two supported protocol versions instead
  of four, and described an unsupported version as an error when it is
  negotiated. Agents read this before writing handlers, so stale guidance here
  becomes wrong code.
- **The docs grew a visual layer**: a capability tour, a five-page MCP
  section, twelve architecture diagrams, and design records for the questions
  people ask (why session-centric, why no `--dry-run`, why an execution
  fabric).

### Verification gates

| Gate | Result |
|------|--------|
| `go build ./cmd/pgmi` | PASS |
| `go test -short ./...` | PASS |
| `golangci-lint run` (native + `GOOS=linux`) | PASS |
| Session API contract (`api-v1.sql`) | unchanged — compat **v1** |
| `go test ./internal/scaffold` (advanced + basic, live PostgreSQL 17) | PASS |
| PostgreSQL matrix — 11, 15, 17 (both compatibility floors) | PASS |
| Platform matrix — Linux, Windows (PowerShell + bash), macOS | PASS |
| CI example projects (`test-gated-deploy`, `execution-order-policy`, `lock-safe-deploy`, `seed-demo`, `tap-reporter`) | PASS |
| `govulncheck ./...` (v1.6.0) | PASS — 0 reachable; 2 unreachable, see below |

Two advisories are reported at the module level and do not affect pgmi:

- **GO-2026-5932**: `golang.org/x/crypto/openpgp` is unmaintained. It arrives
  as an indirect module requirement and has no fixed version; pgmi imports no
  package from it, so no code path reaches it.
- **GO-2026-5841**: `github.com/klauspost/compress/s2` OOB read, fixed in
  v1.18.7. pgmi requires v1.18.5 as a transitive dependency but imports no
  package from it, so no code path reaches it.

Both are listed here rather than suppressed, so the next release can tell
"still not reachable" from "nobody looked".

**macOS is now gated, and fixing it found a real bug.** The `test-platforms`
matrix had `macos-latest` marked `continue-on-error` because it had never been
observed green. Diagnosing that failure turned up a defect in the shipped
gateway rather than a problem with the runner: `tools/mcp-gateway.py` printed
`Listening: http://HOST:PORT` and then refused every connection until the
host's reverse DNS answered. `http.server` fills `server_name` with
`socket.getfqdn(host)` inside `server_bind()`, which runs *before*
`server_activate()` calls `listen()` — so the socket sits bound but not
listening for the whole lookup, with no error to explain it. Anywhere reverse
DNS is slow or absent — a VPN, a container with no resolver, an air-gapped
host — the gateway looked started and served nothing. It now binds without
resolving, `continue-on-error` is gone, and macOS is a blocking check.

What is still untested there is platform-specific behaviour the unit tests do
not reach (signal handling, `.pgpass` location, path separators), and the
`release.yml` tag workflow still runs on Linux only — a tag inherits its
guarantee from `main` being green. If you hit a macOS-only issue, please
report it.

## v0.11.0 — 2026-06-27

**You can now run the advanced template on managed PostgreSQL** — RDS, Aurora, Cloud SQL, Azure Flexible Server, Supabase, Neon — where you do not get a superuser. It previously hard-failed on all of them. The template also publishes an OpenAPI 3.1 contract from its own handler registry, so an API consumer gets a self-describing spec instead of a hand-written client, and `pgmi serve` lets an AI assistant drive pgmi directly over MCP.

The session API contract is **unchanged at v1** — existing `deploy.sql` files continue to work without modification.

### Highlights

- **Advanced template deploys without superuser (PGMI-62).** Entity-table standards (`created_at`/`deleted_at` on tables marked `object_id core.entity_id`) were enforced by a global `ddl_command_end` event trigger, and `CREATE EVENT TRIGGER` requires true superuser — so the template hard-failed on RDS/Aurora, Cloud SQL, Azure Flexible Server, Supabase, and Neon. The event trigger and both superuser gates are gone, replaced by a `pg_temp` catalog-driven reconcile: a mandatory deploy-end sweep enforces the floor, and an optional inline `pg_temp.apply_entity_table_standards('schema.tbl')` call lets a schema file colocate a `WHERE deleted_at IS NULL` partial index. The reconcile machinery lives in `pg_temp` and vanishes at session end.
- **OpenAPI 3.1 from the handler registry (PGMI-80, PGMI-81).** `GET /openapi.json` generates a spec directly from the registered REST/RPC handlers, and every handler now declares input/output JSON schemas (with a coverage signal flagging any that don't). Paired with the consumption-DX work, an API consumer gets a self-describing contract instead of a hand-written client.
- **`pgmi serve` — MCP server over stdio (PGMI-88).** Exposes pgmi's read-only commands as MCP tools so an AI assistant can drive pgmi directly (`claude mcp add pgmi -- pgmi serve`).
- **`pgmi info` — read-only project introspection (PGMI-89).** Reports what a project contains (templates, deploy entrypoint, test layout) without connecting to a database.
- **Structured and friendlier CLI output.** `--json` for machine-readable deploy results (PGMI-84), a deploy summary line with optional verbose per-notice timing (PGMI-85, PGMI-93), color output honoring `NO_COLOR` (PGMI-87), and an auto-pager for long output (PGMI-91).
- **Broader AI-assistant onboarding.** Five assistant setup adapters incl. `GEMINI.md` and an `--all` flag (PGMI-78, PGMI-98), `pgmi ai client [lang]` for per-language consumption guidance (PGMI-82), `pgmi ai skill advanced-template` surfacing the framework API (PGMI-95), a philosophy/anti-pattern FAQ and an agent-facing CLI reference in `pgmi ai` (PGMI-90, PGMI-92).
- **Consumption DX (PGMI-83).** Scalar API explorer, codegen recipes, and ready-to-run HTTP collections.
- **Documentation website.** A Hugo Book site (single-sourced from `docs/`) deploys to GitHub Pages via CI, with a custom landing page, SEO, and mobile fixes.

### Advanced template

- **Admin analytics REST API (PGMI-74)** replaces the demo handlers with a real, authenticated endpoint set.
- **Exchange retention** via a batch `purge_exchanges` procedure with `ASSERT` preconditions (PGMI-76).
- **View-layer test coverage (PGMI-75)** for the membership/API view surface.

### Fixes

- **PG17-only `sha256()` replaced with `extensions.digest`** in `semantic_fingerprint` — the template no longer requires PostgreSQL 17 for that path.
- **`api_key_prefix()` volatility corrected** `IMMUTABLE` → `STABLE`; **`PARALLEL SAFE`** added to membership read-only functions.
- **Redundant index removed** on `rpc_route.method_name`; redundant double cast removed in the JSON-schema validator.
- **Error context throughout the CLI**: bare OS/YAML/connection errors are wrapped with operation context and `ErrInvalidConfig`; the original error is preserved when a deploy is interrupted; config decode errors now name the file.

### Internal

- Hardening pass: `errors.Is`/`errors.As` and sentinel errors replace string matching across deployer, preprocessor, and info paths; dead code removed (`TestConfig` anti-pattern scaffolding, unused `verbose` params, dead connection string-matching).
- Go modernization: `slices`/`cmp` replace `sort`, `maps.Copy`/`maps.Clone` replace manual loops, `strings.Contains` replaces custom helpers.
- Several PL/pgSQL functions converted to `LANGUAGE sql` (`delete_user`, `rest_invoke` overloads, `openapi_path`/`openapi_methods`); basic template switched from `SERIAL` to identity columns.
- `develop` branch retired (snapshot CI trigger dropped); GitHub Pages auto-enabled on first deploy.

### Verification gates

| Gate | Result |
|------|--------|
| `go build ./cmd/pgmi` | PASS |
| `go test -short ./...` (29 packages) | PASS |
| `golangci-lint run` (native + `GOOS=linux`) | PASS |
| Session API contract (`api-v1.sql`) | unchanged — compat **v1** |
| `go test ./internal/scaffold -run TestTemplateDeployment` (advanced + basic, live Postgres) | PASS |

## v0.10.1 — 2026-06-14

28 commits, 63 files changed, +1,206 / -493 lines since v0.10.0.

A patch release: a correctness fix to the advanced template's try-cast operator, a hardened release pipeline, and a documentation/contract accuracy pass. The session API contract is **unchanged at v1**; existing `deploy.sql` files continue to work without modification.

### Highlights

- **Try-cast operator renamed `?|` → `api.?>`** (advanced template, `lib/common/cast.sql`). The old `?|` name collided with PostgreSQL's built-in jsonb `?|` operator, so any handler that loaded both the jsonb operators and the try-cast operators had ambiguous resolution. The operator is now `api.?>`, created in the `api` schema so it resolves inside handler bodies (handler `search_path`: `api, internal, extensions, pg_temp`). Covers `uuid`, `boolean`, `integer`, `bigint`, `numeric`, `interval`, `timestamp`, `timestamptz`.
- **Release pipeline now gated.** Tagging `v*` previously went straight to GoReleaser with no checks. A `verify` job now runs lint, the full test suite, the connection/security suite, and `goreleaser check` *before* the publish job — a broken config or red test fails the gate and skips publishing instead of cutting a bad release.
- **`pgmi ai contract` accuracy.** The machine-readable contract now emits `pgmi_is_sql_file()` and `pgmi_persist_test_plan()`, matching the documented v1 API surface; AI assistants no longer see a contract that's narrower than the docs.

### Fixes

- **`install.sh` now verifies the download checksum**, matching `install.ps1` (the POSIX installer previously skipped integrity verification).
- **`--force` help** corrected to describe the actual 5-second countdown.
- **Exit-code documentation synced** across `docs/CLI.md`, `AGENTS.md`, and `docs/QUICKSTART.md` — codes `15`/`16`/`130` and the real `pgmi <ver> (compat <n>)` version format.
- Numerous documentation corrections: GitHub anchor links, template positioning, `pgmi ai setup` assistant/flag coverage, and `COMMENT ON` across basic and advanced template objects.

### Internal

- Go bumped to 1.25 in all workflows; noop GoReleaser hook removed.
- GoReleaser config modernized to current v2 schema (`archives.formats`, `homebrew_casks`) so `goreleaser check` passes clean in the release gate.
- `.gomodcache/` (local `GOMODCACHE`) added to `.gitignore`.
- `interface{}` → `any`, manual contains loops → `slices.Contains`.
- New contract column-level and default-value drift tests.

### Distribution change

- **Homebrew is now a Cask, macOS-only.** GoReleaser removed the Homebrew *formula* path (`brews`), so pgmi ships as a Cask: `brew install --cask vvka-141/pgmi/pgmi`. **Linux Homebrew is no longer supported** — Linux users should use the APT repo (`dl.cloudsmith.io/.../pgmi`), the install script, a GitHub Releases archive, or `go install`.

### Verification gates

| Gate | Result |
|------|--------|
| `go test -short ./...` (27 packages) | PASS |
| `go build ./cmd/pgmi` | PASS |
| `go test ./...` (full integration) / `goreleaser check` | **gated in CI** (new `verify` job runs before publish) |

### Upgrade notes

- No `deploy.sql` changes required — session API is at v1.
- **Advanced template only:** if you used the try-cast operator by its `?|` name in handler SQL, switch to `api.?>`. The `common.try_cast(...)` function form is unchanged.

## v0.10.0 — 2026-05-04

20 commits, 100 files changed, +4,707 / -1,798 lines since v0.9.1.

This release is the result of a 360° code review and a multi-wave refactor. It pulls in significant security fixes from the Go review (concurrent-deploy hardening, AWS RDS IAM token refresh, symlink rejection on file scanning), unblocks managed cloud Postgres (Wave G removed `plv8`), tightens the entire CLI voice for a Postgres-expert audience (three-tier rewrite of help text, error messages, approver prompts, NOTICE output), and lands a Wave 4 API-key authentication system in the advanced template.

The session API contract (`pgmi_source_view`, `pgmi_plan_view`, `pgmi_test_plan`, etc.) is **unchanged at v1**. Existing `deploy.sql` files continue to work without modification.

### Highlights

- **Cloud-compat verified**: removed `plv8` from advanced template; basic template deploys on all major managed providers. Advanced template requires superuser for the DDL event trigger — see the [cloud compatibility matrix](docs/PRODUCTION.md) for provider-specific details and the one-file workaround. Integration test now exercises the advanced template against a stock Postgres testcontainer end-to-end (19 SQL test steps, idempotent redeploy).
- **API-key authentication** for machine-to-machine access in the advanced template: SHA-256 hashed keys with constant-time compare, tied to the existing `auth.user_identity` provider hierarchy, with disable / re-enable / revoke / expiry / activation-window lifecycle.
- **Entity standards via DDL event triggers**: `core.entity_id` UUID domain marker triggers a superuser-escalated event handler that uniformly stamps `created_at`, `updated_at`, `deleted_at`, ownership columns, and audit triggers across every table that uses the marker. Replaces the old inheritance-based approach.
- **REST/RPC/MCP routing**: handler registry (`api.handler`) backs all three protocols, with JSON-RPC 2.0 envelope semantics, MCP `tools/call` spec compliance (`result.isError`, `structuredContent`, `_meta.tags`, RFC 6570 URI templates), and the same tag-based dispatch wiring across protocols.

### Security & correctness fixes (Wave H)

- **`pg_try_advisory_lock` for concurrent deploys**: two simultaneous `pgmi deploy` runs against the same database now fail fast with `ErrConcurrentDeploy` (exit code 15) instead of corrupting each other's session state.
- **AWS RDS IAM token refresh**: tokens previously expired after 15 minutes mid-deploy on long-running migrations because pgxpool dialed new backends with stale credentials. New `BeforeConnect` hook re-mints tokens on every backend dial; `MaxConnLifetime` capped to remaining token validity.
- **Symlink rejection on file scanning**: `*.sql` symlinks pointing at `/etc/shadow` (or any path outside the project) are now rejected at the loader. `os.Lstat` + `io.LimitReader` + post-read size verification.
- **Macro detection respects string literals and comments**: the preprocessor used to expand `CALL pgmi_test()` even when the text appeared inside an `EXECUTE` quoted string or a `RAISE NOTICE`. New `RedactForMacros` produces a length-preserving mask so byte positions stay correct; literals and comments are blanked out before the regex scan.
- **Race-free signal handling**: `--force` and signal interrupts now use `sync/atomic.Bool` instead of plain bool. `go test -race` clean.
- **Password redaction in `FormatError`**: libpq `password=...` and URI `user:pass@host` fragments are scrubbed before any error message reaches the user.

### Cloud compatibility (Wave G)

- `plv8` extension dependency removed from the advanced template. Required extensions are now `uuid-ossp`, `pgcrypto`, `pg_trgm`, `hstore` — all available on managed clouds.
- Cloud compatibility matrix added to `docs/PRODUCTION.md` covering RDS / Aurora, Azure Flexible Server / Cosmos, Google Cloud SQL / AlloyDB, Supabase, Neon.
- Integration test gate that previously skipped the advanced template when `plv8` was unavailable has been removed; the advanced template is now actually exercised in CI.

### CLI voice overhaul (Tier 1, 2, 3)

A three-tier rewrite of every user-facing string against an explicit voice guide for Postgres experts, CLI experts, and migration engineers. **No breaking changes to flags, exit codes, or behavior** — only message text.

**Tier 1 (high-visibility surfaces):**
- Root `pgmi --help`: dropped marketing prose, philosophy paragraph, and exit-code wall. New help is 13 lines: one mechanical description, three example commands, libpq env-var line. Empty `pgmi` (no args) shows the ASCII logo + brief on TTY only; pipes/CI/`PGMI_NO_BANNER=1` get the clean help fallback.
- `pgmi deploy --help`: tightened from 40+ lines to 23, exit codes documented here (where troubleshooters look), parameter precedence rule one-liner.
- `pgmi init --help`: 12 lines, examples-first, fixed output stream discipline (status to stderr, file tree to stdout).
- Connection error messages tightened from 7-9 lines each to 2-3 lines while preserving `errors.Is` chain via a custom `connError` type.

**Tier 2 (subcommands, approvers, init wizard):**
- `templates`, `metadata`, `ai`, `version`, `config`, `completion` subcommand help all rewritten.
- Template descriptions corrected — `basic` and `advanced` now show their actual current structure, not the year-old stale layout.
- Interactive approver: `WARNING: about to DROP and RECREATE database "mydb". This deletes all data. Type the database name to confirm:` — Postgres-style identifier quoting, no emoji, no redundancy.
- `--force` ASCII danger banner: kept as a safety-critical interruption surface, but now TTY-gated (same gate as the empty-command logo). Plain countdown in CI/pipes.

**Tier 3 (logger, NOTICE prose, connection wizard):**
- Three "successfully" violations removed from deployer logger output.
- Single-quoted DB names → Postgres-style `%q` throughout deployer/session.
- DROP/CREATE DATABASE log lines now print the actual SQL statement so operators can correlate with PostgreSQL state.
- Connection wizard: `✓ Connected successfully` → `✓ Connected`. The cloud-auth pseudo-success message now honestly reports `X authentication configured (not tested — token is fetched at deploy time)` instead of misleading "ready" prose.

### CLI infrastructure

- **New exit codes**: `15` (ExitConcurrentDeploy), `130` (ExitInterrupted, SIGINT/Ctrl-C convention).
- **`pgmi metadata`**: `scaffold`, `validate`, `plan` subcommands operate purely on the filesystem (no DB connection) for inspecting `<pgmi-meta>` blocks, validating XML/uniqueness, and previewing execution order.
- **`pgmi ai`**: machine-readable documentation surface for AI coding assistants (overview, skills, templates) — designed for Claude Code, Copilot, Gemini, Cursor to ingest as context.
- **`-d/--database` reserved for target**: connection-string database is the *maintenance* database, `-d` is the *target*. Two-database pattern documented and tested.
- **`pgmi version` machine-greppable**: `pgmi version | head -1` returns just `pgmi 0.10.0 (compat 1)`.

### Behavioral changes worth flagging

- **`pgmi init` output streams**: status messages (`Wrote ./demo (basic template).`, `Next:`) now go to **stderr**; the file tree stays on stdout. Scripts that captured init output verbatim may need adjustment (no users yet, but flagged).
- **Connection error messages no longer print "Original error: %w" trailer**. Visible message is the curated 2-liner; the original error is still in the chain via `errors.Unwrap()`.
- **Connection wizard for non-standard auth (Azure/AWS/Google) no longer claims a successful connection**. It now correctly reports that the configuration was saved and the token will be fetched at deploy time.
- **`pgmi templates list` and `templates describe` now emit usage hints to stderr**, structured data to stdout. `pgmi templates list | awk` works as intended.

### Documentation

- New `docs/API-KEYS.md` documenting the Wave 4 authentication system.
- Cloud compatibility matrix in `docs/PRODUCTION.md`.
- 10 stale documentation references corrected across `docs/`.
- AI-digestible content in `internal/ai/content/` synced from `.claude/skills/`.
- New embedded skill `pgmi-cli-voice` documenting the voice rules going forward.

### Test improvements

- Advanced template integration test: previously gated on `plv8` (silently skipped on stock Postgres), now runs unconditionally and verifies a clean deploy + idempotent redeploy + 19 SQL test steps.
- 4 new test cases for `ExitInterrupted` (130) and `ExitConcurrentDeploy` (15) sentinel mappings.
- Connection-scenario suite (`internal/db/conntest`): 14 tests covering mTLS, sslmode precedence, SSL modes, standard auth — all green.

### Internal

- ASCII logo asset retained, exposed only on the empty `pgmi` command in interactive TTY contexts. `PGMI_NO_BANNER=1` provides explicit operator opt-out.
- `--force` skull asset retained, gated identically. The skull is a safety-critical interruption surface, not decoration; visual jolt is the feature.

### Verification gates

All of the following pass on this branch:

| Gate | Result |
|------|--------|
| `go test -short ./...` (27 packages) | PASS |
| `go test ./...` (full integration, testcontainer) | PASS |
| `go test -tags conntest ./internal/db/conntest/...` (14 scenarios) | PASS |
| `go test ./internal/scaffold -run TestTemplateDeployment` (basic + advanced) | PASS |
| `golangci-lint run ./...` (Windows) | clean |
| `GOOS=linux golangci-lint run ./...` | clean |
| Cross-platform builds (Windows amd64, Linux amd64+arm64, macOS amd64+arm64) | clean |
| `go test -race ./...` | **deferred to CI** (Windows host has no gcc; gate must run on Linux) |

### Upgrade notes

- No `deploy.sql` changes required — session API is at v1.
- If you script `pgmi init` output: status text moved to stderr.
- If you have automation that grepped specific error message text, two rewordings to be aware of: connection errors are shorter; connection-wizard cloud-auth status is now honest about not having tested.

---

## Earlier releases

See `git log --tags --oneline` for the v0.7 / v0.8 / v0.9 history. This file starts the explicit per-release changelog at v0.10.0.
