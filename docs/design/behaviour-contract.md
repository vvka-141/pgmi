---
title: "pgmi behaviour contract"
description: "Contributor reference: what any pgmi binary must do, seen from outside, with the black-box cases that check each behaviour."
weight: 49
---

# pgmi behaviour contract

What any pgmi binary must do, seen from outside. Written from the Go binary
(v0.14.1) for the Rust re-implementation (PGMI-826), and kept thin on purpose:
the black-box suite in `blackbox/` is the executable form of this document.
Each section states the behaviour in a few lines, names the user document that
already describes it in full where one exists, and names the cases that check
it. A behaviour no case checks says why.

Case names are paths under `blackbox/cases/` without `.yaml`.
`blackbox/selftest/test_contract.py` checks that every case is cited here, that
every cited case exists, and that each section names cases or a reason.

The sections under **Intended changes** and **Decided changes** describe where
the Rust binary differs from Go on purpose; **Behaviour changes in 0.20** lists
them with their exit codes. A case that observes one keeps Go's expectation and
states the Rust one under `expect.rust` (`blackbox/README.md`).

## Exit codes

### Exit code table

0 success; 1 general error (the only known trigger: a filesystem failure);
2 usage error (unknown command or flag, wrong arity, bad flag value such as a
`--timeout` that is not a duration); 3 internal panic; 10 invalid pgmi
configuration, refused before any database is created, dropped or written;
11 connection failed, including a `--timeout` that runs out while connecting;
12 overwrite not approved; 13 SQL failed; 14 `deploy.sql` missing; 15 another
deploy holds the target; 16 `--timeout` exceeded after connecting; 130 SIGINT
or SIGTERM. Full table: `docs/CLI.md` "Exit Codes".
An error is one stderr line starting `pgmi: error: `. Usage errors include an
unknown subcommand, an unknown shorthand, and `-h` without a value on
`deploy` (it is `--host`; the message says so). More than one of `--azure`,
`--aws`, `--google` is exit 10. Booleans take `=false`, `--param` is never
split on commas, and `--timeout 0` turns the timeout off; help shows the
default as `3m0s`.

Cases: `commands/usage-errors`, `exit-codes/0-success`, `exit-codes/2-usage`, `exit-codes/10-invalid-config`, `exit-codes/11-connection-failed`, `exit-codes/12-overwrite-denied`, `exit-codes/13-sql-failed`, `exit-codes/14-no-deploy-sql`, `exit-codes/15-concurrent-deploy`, `exit-codes/16-timeout`, `exit-codes/130-interrupted`, `exit-codes/130-sigterm`, `commands/init-unwritable`, `commands/short-help-and-sslmode-value`.
Exit 3: no case; a release binary cannot panic on purpose. The Rust build proves it with its `testhooks` feature (`rust/pgmi/tests/panic.rs`): a panic exits 3 with the `--json` record, and a panicking serve tool answers `isError` with exit code 3.

## Commands

### info

`pgmi info [path]` summarises a project without a database: template, whether
`deploy.sql` and `pgmi.yaml` exist, file counts, directories; `--json` gives
the same as an object. A missing path is exit 10.

Cases: `commands/info`, `commands/info-scaffolded-by`.

### init

`pgmi init [path] [-t minimal|basic|advanced]` writes a template. Without `-t`
and without a terminal it writes `minimal`; on a terminal it runs the wizard. A
file named `<name>.scaffold` is written as `<name>`. A target holding anything
but `pgmi.yaml`, `.env` or `.git` is exit 2; an unknown template is exit 2.
`{{PROJECT_NAME}}` in a template becomes the target folder's name, as is in
text files and JSON-escaped in `.json` files. Answering Yes to "Set up the
connection now?" runs the `config` wizard and writes its settings into the
scaffolded `pgmi.yaml`.

Cases: `commands/init`, `commands/init-unwritable`, `terminal/init-wizard`, `terminal/non-interactive`, `commands/init-project-name`, `terminal/init-wizard-connection`, `terminal/init-wizard-connection-esc`.

### Template deployments

The basic and advanced templates, as `init` writes them, deploy into a new
database with `--param env=dev` and redeploy over it without error. The
templates themselves are shared files (`shared/templates/`), so this checks the
binary's half: loading, the session API, the execution contract and the test
macro under real projects.

A function in `public` owned by another role, such as a hosting provider's,
does not fail the basic template's privilege test. Before anything changes, a
first advanced deploy names what the login lacks (`CREATEDB`, `CREATEROLE`)
and, when the server has Azure's `azure.extensions`, the template's extensions
missing from it. On a database with the role `databricks_superuser` and the
schema `__db_system` (Lakebase), `provider_role_pattern` and
`provider_schemas` default to Lakebase's values, and the `env=dev` admin password is a generated one (record 0024).

Cases: `deploy/basic-template-converges`, `deploy/basic-template-provider-functions`, `deploy/advanced-template-converges`, `deploy/advanced-template-preflight`, `checksums/cross-binary-redeploy`, `checksums/cross-binary-redeploy-advanced`.

### metadata

`metadata plan` prints the rows `pgmi_plan_view` will hold, in order (`--json`
too); `metadata validate` checks every `<pgmi-meta>` block and refuses a bad id
or a duplicate id with exit 10, naming the files; `metadata scaffold` previews
blocks for files without one and writes them with `--write`, honouring
`--sort-key` and `--idempotent`. Full reference: `docs/METADATA.md`.

The block is the one block comment (`/* */`, nested allowed) containing
`<pgmi-meta` (case-sensitive); one inside a `--` comment or a dollar quote is
not metadata. An id may be hyphenated, braced, `urn:uuid:` or 32 hex digits,
any letter case and any uuid version; it is stored lower-case hyphenated.
`idempotent` takes the boolean spellings `1`, `t`, `T`, `TRUE`, `true`,
`True` and their false forms, white space trimmed. Unknown elements and
attributes, an XML prolog and a default namespace are ignored; standard and
numeric entities and CDATA are decoded; repeated `<sortKeys>` are merged;
text after the root element is ignored; a sort key keeps its white space
and may repeat. Refused with exit 10, naming the file: `idempotent` missing
or not a boolean, a missing or nil id, an id with spaces, a blank sort key
or description, an unknown entity, unclosed XML, two blocks (in the Rust
binary also two `<pgmi-meta` elements in one comment, see "Two metadata
elements in one comment are refused"), a block over
10240 bytes. A file without a block gets the id `md5("./<path>")` read as a
uuid, the same in `metadata plan` and `pgmi_plan_view.generic_id`.

Cases: `commands/metadata`, `commands/metadata-invalid`, `commands/metadata-duplicate`, `commands/metadata-scaffold-run-once`, `commands/metadata-forms`, `commands/metadata-refusals`, `commands/metadata-scaffold-and-checks`.

### templates, version, ai, completion, help

`templates list|describe`, `version` and `--version` (`pgmi <version> (compat
1)`), `ai` (the overview) and `ai contract` (the session API), `completion
bash|zsh|fish|powershell`, `help <command>` and `--help`. An unknown
subcommand, template or shell, or extra arguments, is exit 2. `pgmi` with no
arguments prints the root help on stdout and exits 0; on a terminal it draws
the banner on stderr instead, unless `PGMI_NO_BANNER` is set. `deploy --help`
describes the command, its examples, where the password and parameters come
from, and every flag. Completion offers `init --template` names and
`--sslmode` values in every shell; in PowerShell the Rust script completes
them itself (Rust unit test
`powershell_completes_template_names_and_sslmode_values`), where Go's script
calls back into the binary.

Cases: `commands/templates`, `commands/version`, `commands/ai`, `commands/completion`, `commands/help`, `commands/bare`, `commands/deploy-help-text`.

### Verbose output

`-v` shows `RAISE DEBUG` from `deploy.sql` and lists ignored paths; without it,
DEBUG is hidden and NOTICE, INFO and WARNING are shown. With `-v` every notice
line (DETAIL and HINT included) starts with the seconds since the deploy
started, `[1.23s] `. No server DEBUG line from pgmi's own queries reaches
stderr: the message level is raised only after `CALL pgmi_test()` is expanded.
Parameter loading prints counts only, never a key or a value:
`[VERBOSE] Loaded N parameter(s) from PGMI_PARAM_* environment variables`,
per params file `[VERBOSE] Loading parameters from file: <path>` and
`[VERBOSE] Loaded N parameter(s) from file (total: M)`, and
`[VERBOSE] CLI parameters override N value(s)`.

Cases: `commands/verbose`, `notices/verbose-elapsed`, `notices/verbose-no-internal-debug`, `params/verbose-counts`.

### serve

`pgmi serve` is an MCP server over stdio (JSON-RPC 2.0, one message per line):
`initialize`, `tools/list` with seven tools (`deploy`, `init`, `metadata_plan`,
`metadata_validate`, `templates_list`, `ai_overview`, `ai_contract`),
`tools/call`, `ping`. A malformed line gets -32700 with a null id, an unknown
method -32601, and the session goes on. Calls run concurrently: a ping is
answered during a deploy, and `notifications/cancelled` stops a running deploy,
which rolls back. End of input exits 0. Full reference: `docs/CLI.md`
"pgmi serve".

Once input ends or SIGINT/SIGTERM arrives and the running calls have ended,
a client that no longer reads gets 2 s to take the remaining answers before
serve exits. A failed write to stdout ends the session: running calls are
cancelled and nothing more is read (Go stops reading only when an answer
written by the reader fails, and then waits for the running calls). Rust unit tests
`a_client_that_stops_reading_does_not_hold_up_the_stop`,
`an_output_failure_ends_the_session`.

Malformed input, each answered and the session continuing: text that is not
JSON, including invalid UTF-8 outside a string, is -32700 with a null id;
JSON that is not a request object (`[]`, a number, a string) or has a member
of the wrong type (`"jsonrpc": 2`) is -32600 with a null id; a missing or
wrong `jsonrpc` is -32600 echoing the id; an id that is an object, array,
boolean or null is -32600 with a null id; `tools/call` without params or with
params that are not an object is -32602, an unknown tool -32602; wrong tool
arguments are a tool result with `isError` and the exit code (10 for a
missing path or an unknown argument). Blank lines and notifications get no
answer; a 2 MB line is read (the Rust binary reads up to 16 MiB, see "serve
bounds what a client can make it hold"); invalid UTF-8 inside a string reads
as U+FFFD.
String and number ids (`1.5`) are echoed as sent. The bare value `null`
and an id holding invalid UTF-8 differ between the binaries: see "serve
answers a bare null and never echoes invalid bytes" under Decided changes.

`initialize` needs `protocolVersion` (-32602) and echoes `2025-11-25`,
`2025-06-18` or `2024-11-05`, answering any other version with `2025-11-25`.
A request whose `_meta` names protocol version `2026-07-28`, and
`server/discover` always, is served statelessly: results carry
`resultType: "complete"` and the server info in `_meta`, `tools/list` adds
`ttlMs` 3600000 and `cacheScope`, a missing client-capabilities field is
-32602, another version is -32022 with the supported list, and `initialize`
and `ping` are -32601.

The deploy tool never reads the project `.env`; `overwrite` without a
matching `confirmDatabaseName` is exit code 12 before connecting. Each deploy
holds the deploy lock only for its own run: deploys one after another on one
serve process succeed even after one ended inside an open transaction or
failed in a tail block, and two overlapping deploys to one database give the
second exit code 15. The `timeout` argument takes Go duration syntax (default
3m): running out is exit code 16 with the statement cancelled on the server,
a value that is not a duration exit code 10. Cloud sign-in reaches serve only
through `pgmi.yaml` and the environment; the tool has no cloud arguments.
The deploy tool's text lists each kept `WARNING` as `WARNING: message`, one
per line, after the summary line on success and after the exit code on
failure. A `params` key that is not an identifier (one holding `=` too) is
exit code 10 before connecting. A SQL `PASSWORD '...'` literal in a failed
deploy's error is `PASSWORD [redacted]` in the text, `message` and `error`.

Cases: `serve/session`, `serve/deploy-tool`, `serve/cancel`, `serve/malformed`, `serve/protocol-versions`, `serve/deploy-guards`, `serve/deploys-in-sequence`, `serve/concurrent-deploys`, `serve/deploy-timeout`, `serve/deploy-warning-lines`, `serve/deploy-param-key`, `serve/deploy-password-masked`.

### config

`pgmi config [path]` is a terminal wizard: it asks before replacing the
connection settings of an existing `pgmi.yaml`, tests the connection, writes
`pgmi.yaml` keeping the file's comments and other keys, and offers to save the
password to `~/.pgpass`. A `pgmi.yaml` that does not parse is replaced only
after a confirmation. Without a terminal it is exit 2. Declining a prompt or
Esc on the first screen is exit 12; Ctrl-C in the wizard is exit 130.

Cases: `terminal/config-wizard`, `terminal/prompt-cancel`, `terminal/config-refusals`, `terminal/config-replace-unparseable`, `terminal/config-esc`.

## Deploy

### Project discovery and loading

Every file under the project path enters the session except the built-in
exclusions, dot-files and `.pgmiignore` patterns (`.gitignore` syntax: comments,
globs, classes, `**`, directory patterns, `!`, escapes). A pattern set that
would hide `deploy.sql` is exit 10, and so is a bracket class that is empty
(`[]`, `[!]`, or `]` right after the opening bracket) or holds a reversed
range (`[z-a]`); an unclosed `[` is a literal bracket. A leading UTF-8 BOM is stripped from every
file before anything else sees it. Each file is capped at 10 MiB
(`PGMI_MAX_FILE_SIZE` overrides; it must be a positive whole number of bytes,
else exit 10). Full reference: `docs/DEPLOY-GUIDE.md` "What gets loaded".

The walk never follows a symbolic link: a linked file or directory that
`.pgmiignore` does not hide is exit 10 naming it; `deploy.sql` itself may be
a link (`failed to read ./x.sql: refusing to read symlink x.sql: pgmi does
not follow symlinks; ...`). A file containing a NUL byte (`file ./x.bin
contains a NUL byte; ...`) or invalid UTF-8 is exit 10 naming it (for
`deploy.sql`, with the byte offset). A directory named `__<name>__`
other than `__test__` and `__tests__` is exit 10. A file larger than the cap
is exit 10. A named pipe, socket or device that `.pgmiignore` does not hide,
`deploy.sql` included, is exit 10 (`refusing to read <path>: not a regular
file (a named pipe, socket or device)`) and is never read, in `deploy` and in
`info`. The first SIGINT or SIGTERM during the load is exit 130 and a
`--timeout` that runs out there is exit 16, as in Go, which loads after
connecting.

Cases: `dialect/pgmiignore`, `dialect/pgmiignore-hides-deploy`, `execution/loader-at-scale`, `dialect/project-walk`, `commands/metadata-refusals`, `errors/special-file`, `dialect/nested-deploy-sql-loaded`, `dialect/pgmiignore-broken-patterns`. The load racing a signal and the timeout: Rust unit tests `blocking_work_gives_way_to_the_first_signal`, `blocking_work_gives_way_to_the_timeout`.

### Session API

The temporary tables, views and functions `deploy.sql` sees are defined by
`shared/session/schema.sql` and `shared/contract/api-v1.sql`; every binary
embeds these files unchanged, so they are the contract. `pgmi ai contract`
prints the same as JSON. `--compat 1` selects this interface version (the only
one); any other value is exit 10, checked after the connection and the
project are resolved and before connecting, so the summary line and the
`--json` target, `connectionSource` and `database` are there, with
`contractVersion` null. Plan order: sort key, then path, byte order; a file
without metadata sorts by its path; a missing `<description>` is an empty
string. The session tables are analysed after loading, so deploy.sql and
pgmi's own plan queries see statistics (case `deploy/session-tables-analyzed`). Full reference: `docs/session-api.md`.

The session speaks the target database's own encoding (`client_encoding` is set
to `server_encoding`), as Go's driver does by never sending one: on a LATIN1
database every byte pgmi sends, paths and file text included, is stored as
sent, so non-LATIN1 characters never fail with 22P05 and `generic_id` is the
md5 of the path's UTF-8 bytes on every encoding. A `client_encoding` the user
names (connection string, or `PGCLIENTENCODING` in the Rust binary) wins over
this; see "Every accepted connection key is honoured or refused".

Cases: `execution/loader-at-scale`, `params/precedence`, `commands/ai`, `flags/compat`, `flags/compat-record`, `deploy/latin1-database`.

### Parameters

Sources, highest first: `--param`, `--params-file` (later files win; `-` reads
stdin, at most once), `PGMI_PARAM_*` variables (the project `.env` is loaded
first and never overrides a variable already set), `pgmi.yaml` `params`. Keys
are lower-cased. Each reaches `deploy.sql` as `current_setting('pgmi.<key>')`
and in `pgmi_parameter_view`. Refused with exit 10: an empty `PGMI_PARAM_*`, a
`--param` without `=`, a missing params file, two keys differing only in case
within one source (one file, the `--param` flags, the `PGMI_PARAM_*`
variables, `pgmi.yaml` `params`; across sources they are one parameter and the
higher source wins), a `PGMI_PARAM_` with nothing after the prefix, a
`PGMI_PARAM_*` variable that is not valid UTF-8, a key that is not a
PostgreSQL identifier of 1-63 characters (letter or underscore first, then
letters, digits, underscores), and a value holding a NUL byte. All are refused
before connecting. Full reference: `docs/CLI.md`
"Parameter Flags".

Cases: `params/env-var-reaches-session`, `params/precedence`, `params/invalid-key`, `params/case-twins`, `flags/params-file`, `config/precedence`, `config/dotenv`, `exit-codes/10-invalid-config`.

### Connection sources

The connection comes from `--connection`, else `PGMI_CONNECTION_STRING`, else
`DATABASE_URL`, else `-h/-p/-U` with the `PG*` variables, else `pgmi.yaml`,
else defaults (localhost:5432, maintenance database `postgres`). A connection
string may be a URI, libpq keyword/value (spaces around `=`, quoted values
holding `;`) or ADO.NET (a value quoted with `'` or `"`, a doubled quote
inside; a port in `Host=h:port` wins over `Port=` wherever it stands). `-d` (or `PGDATABASE`, or
`pgmi.yaml` `database`) names the target; the string's database is the
maintenance database. Refused with exit 10: `--connection` with `-h`, `-p` or
`-U`; `-h`, `-p` or `-U` next to `PGMI_CONNECTION_STRING` or `DATABASE_URL`;
`--connection` with `--sslmode`; `PGSERVICE` or `service=`; an unknown key in
`pgmi.yaml`. Unless typed on the command line, stderr says where the connection
came from (`connection: from <source>`, or `connection: defaults`), with
`(set by <path>)` after the name when the project `.env` set that variable.
Then, on stderr whatever the source: `<path> applied: <keys>` (the keys `.env`
set, never their values; `<path>` starts `./` when the project path is
relative), one line per `pgmi.yaml` connection value another source replaced
(`<field>: from <source> (<value>), not pgmi.yaml (<value>)`, also in `--json`
`overrides`) or that a connection string drops (`sslmode: pgmi.yaml (...) does
not apply to the connection string from <source>; set sslmode in the string`),
and `session options: from PGOPTIONS` (never the value). A failure to connect,
or to resolve the connection, ends with `(connection from <source>)` or
`(connection defaults)` unless the connection was typed; the `--json` `error`
carries that line only when resolving failed. Full reference: `docs/CLI.md`
"Precedence", `docs/CONFIGURATION.md`, `docs/CICD.md` "What the log shows".

Cases: `flags/connection-forms`, `connection/string-forms`, `flags/granular-flags`, `env/connection-string-vars`, `env/connection-string-vars-refuse-flags`, `env/pg-variables`, `env/pgservice-refused`, `config/pgmi-yaml`, `config/pgmi-yaml-invalid`, `config/precedence`, `config/overrides`, `config/dotenv-provenance`, `errors/connection-source`, `env/passfile-beats-pgpassfile`.

### Connection resolution

libpq-style: `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`,
`PGAPPNAME` (default `application_name` is `pgmi`), `PGCONNECT_TIMEOUT`,
`PGPASSFILE`, `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGCHANNELBINDING`,
`PGREQUIREAUTH`, the `PGSSL*` variables (`PGSSLSNI` and `PGSSLCERTMODE`
included), `PGMINPROTOCOLVERSION` and `PGMAXPROTOCOLVERSION`; `PGHOSTADDR` and
`PGSSLCRL` are ignored. Each variable is read as its connection-string key
and refused the same way (exit 10 before connecting); `PGSSLCERTMODE` set but
empty is refused too. `~/.pgpass` (or `PGPASSFILE`) supplies a missing password, wildcards
allowed; a file readable by group or others is ignored with a WARNING. Several
hosts are tried in order and the error names every one tried;
`target_session_attrs` takes `any`, `read-write`, `read-only`, `primary`,
`standby`, `prefer-standby`. `options=` reaches the server. A connection that
fails transiently (SQLSTATE class 08, 53300, 57P03, refused, reset, broken pipe,
closed early, unreachable, timeout, a temporary name-lookup failure) is tried 4 times in all, waiting about 0.1 s, 0.2 s, 0.4 s, each line
on stderr; other failures are not retried. `max_protocol_version` and
`min_protocol_version` (`3.0`, `3.2`) are accepted, and a deploy and a cancel
work with both set to `3.2` (Go speaks 3.2 then; the Rust binary refuses a
minimum above 3.0, see "A minimum protocol above 3.0 is refused for now");
`sslsni`, `sslcertmode` and
`client_encoding` are accepted.

Cases: `env/pg-tls-protocol-variables`, `connection/pgpass`, `connection/pgpass-wildcard-and-file`, `connection/pgpass-world-readable`, `connection/multi-host`, `connection/target-session-attrs`, `connection/keywords`, `connection/unix-socket`, `env/pg-variables`, `env/pgappname`, `env/pgconnect-timeout`, `env/ignored-variables`, `errors/connection-failures`, `exit-codes/11-connection-failed`, `connection/protocol-version`.

### TLS

sslmode `disable`, `allow` (plaintext), `prefer` (TLS when the server offers
it), `require` (no verification, unless a root certificate is given, which makes
it `verify-ca`), `verify-ca` (chain only), `verify-full` (chain and host name;
an IP address needs an IP SAN). The root certificate is `sslrootcert`, else
`~/.postgresql/root.crt`, else the platform trust store (as pgx does; libpq
would refuse); `sslrootcert=system` names the platform trust store
explicitly. `SSL_CERT_FILE` replaces the platform store. The client certificate is `sslcert`/`sslkey`, else
`~/.postgresql/postgresql.crt`/`.key`; an encrypted key opens with
`sslpassword`, a wrong one is exit 10. Each of the three paths comes from
the flag, else the connection string, else `PGSSL*`, else `pgmi.yaml`, with or
without a connection string. `channel_binding=require` needs TLS;
`sslnegotiation=direct` is supported; `require_auth` refuses another method.
`--sslmode` replaces the sslmode of a string from the environment. Token
sign-in (cloud) refuses `disable`, `allow` and `prefer` with exit 10. When that
sslmode came from `pgmi.yaml`, the Rust binary names the line and the fix
(`sslmode: verify-full`, or delete the line). `pgmi init` writes no sslmode
(owner 2026-11-11, PGMI-873; case `cloud/token-auth-pgmi-yaml-sslmode`). `--json`
reports `tls.enabled`, `tls.version` (`TLS 1.3`), `tls.sslmode`.

Cases: `tls/sslmode-matrix`, `tls/sslmode-refusals`, `tls/default-files`, `tls/default-files-good-root`, `tls/tls-only-and-plain-servers`, `tls/client-certificate`, `tls/sslmode-flag-overrides`, `tls/channel-binding-and-direct`, `tls/system-roots`, `tls/verify-without-root`, `tls/encrypted-client-key`, `tls/encrypted-pkcs8-client-key`, `tls/require-auth-and-version`, `tls/yaml-certificates-with-string`, `cloud/token-auth-requires-tls`, `cloud/token-auth-pgmi-yaml-sslmode`.

### Target database, overwrite and approval

A missing target is created (`Database "<name>" does not exist; creating`); a
`CREATE DATABASE` that finds the database already created by another deploy is
exit 15. A server older than PostgreSQL 15 is refused with exit 10 before
anything is created or dropped.
`--overwrite` drops and recreates it: on a terminal after the user types the
database name (a wrong name is exit 12); with `--force` after a countdown, with
a danger banner on a terminal unless `PGMI_NO_BANNER` is set, and without a
countdown when there is no terminal; without `--force` and without a terminal
it is refused with exit 12 and nothing is dropped. `--force` alone changes
nothing. End of input at the name prompt is exit 12 (`No input available`);
SIGINT at the prompt or during the countdown is 130 and drops nothing;
`--timeout` covers them too, and running out there is exit 16 and drops
nothing. A `--timeout` that runs out during the overwrite's catalog queries
is 16. Once the drop has begun nothing interrupts it: the create and the
restore finish (under their own 60-second bound, then cancelled on the
server), and the run then reports the signal (130) or the timeout (16); the
summary says `recreated database "<db>", left empty` and `--json` has
`recreated: true`.
The recreated database keeps its owner, connection limit, database and
role-in-database settings, comment and privileges; a deploying role that
could not put one of them back is refused with exit 10 naming each item, and
nothing is dropped. A database PostgreSQL marked invalid (connection limit
-2, left by an interrupted `DROP DATABASE`) comes back with no connection
limit. Full reference: `docs/CLI.md` "--overwrite".

Cases: `exit-codes/12-overwrite-denied`, `flags/overwrite-force`, `flags/overwrite-invalid-database`, `flags/overwrite-new-database`, `flags/overwrite-restores`, `terminal/overwrite-confirmation`, `terminal/force-banner`, `terminal/force-countdown-timeout`, `terminal/non-interactive`, `terminal/prompt-cancel`, `connection/maintenance-database-only-when-needed`. A signal after the drop: Rust integration test `a_signal_after_the_drop_still_recreates_and_restores_the_database` (a case cannot hold `CREATE DATABASE` on a lock).

### Concurrency

A deploy holds a session advisory lock keyed on the target database for its
whole run; a second deploy to the same target is exit 15. The lock is released
on every path, including a failure and a `deploy.sql` that ends inside an open
transaction.

`--overwrite` also takes a lock keyed on the target name, on the maintenance
database, before it looks at the target, and holds it until the deploy ends: a
second `--overwrite` of the same target is exit 15 (`another pgmi --overwrite
of "<db>" is running`) and drops nothing. The check for a running deploy comes
after the approval, right before the drop: a deploy that started during the
prompt or the countdown is not ended, and the overwrite is exit 15.

Cases: `exit-codes/15-concurrent-deploy`, `execution/open-transaction`, `execution/tail-block-fails`, `flags/overwrite-concurrent`, `flags/overwrite-deploy-during-approval`. Rust integration test `a_held_overwrite_lock_refuses_a_second_overwrite_with_exit_15`.

### Execution contract

Everything up to the first top-level transaction terminator is sent as one
unit and runs as one transaction (the head); a `deploy.sql` without one is
entirely atomic. After it, each top-level statement runs on its own with
autocommit (the tail), so `CREATE INDEX CONCURRENTLY` works and an explicit
`BEGIN ... COMMIT` forms a real transaction. A deploy that ends inside an open
transaction fails with exit 13 and rolls it back. `--json` reports
`executionMode` (`atomic` or `psql`) and `executionUnits`/`unitsCommitted`. Full
reference: `docs/DEPLOY-GUIDE.md` "Atomic mode, then psql mode".

A head that ends with `COMMIT AND CHAIN` or `END AND CHAIN` counts as committed
although the session is never idle after it; a `deploy.sql` that ends right
there succeeds, and the empty chained transaction is rolled back.

Cases: `execution/atomic-head`, `execution/tail-autocommit`, `execution/open-transaction`, `execution/tail-block-fails`, `execution/commit-and-chain`.

### Test macro

`CALL pgmi_test()`, `CALL pgmi_test('pattern')`, `CALL pgmi_test('pattern',
'callback')`, also schema-qualified and in any letter case, is expanded before
`deploy.sql` reaches the server into savepoint-isolated test runs of the
`__test__/` files; never inside comments, string literals or dollar quotes.
Outside an explicit transaction block it fails with 25P01. `--json`
`testMacros` counts the expansions. Comments may stand between the macro's
tokens; only ASCII white space (space, tab, CR, LF, FF) separates them, so
after a no-break space or a vertical tab the text reaches the server
unexpanded. `NULL` or `''` as the pattern means every test, and `NULL` or `''`
as the callback means the default one; a callback that is not a
`[schema.]function` identifier is refused with exit 10. Full reference:
`docs/TESTING.md`.

When SQL test files are loaded (fixtures count) but `deploy.sql` expands no
macro and does not contain `pgmi_test` anywhere (any case), stderr says `N test
file(s) loaded; deploy.sql never calls pgmi_test(), so none of them run` after
`Executing deploy.sql`; the deploy goes on.

Cases: `dialect/pgmi-test-macro`, `execution/pgmi-test-needs-begin`, `execution/pgmi-test-in-block`, `dialect/pgmi-test-macro-comments`, `dialect/pgmi-test-macro-whitespace`, `dialect/pgmi-test-macro-arguments`, `dialect/call-glued-to-a-non-ascii-letter`, `notices/unused-tests`

### Notices and the summary line

Notices are written to stderr as they arrive, while the statement is still
running, in psql's layout: NOTICE, INFO and DEBUG as the bare message, WARNING
and above as `WARNING:  <message>`, then `DETAIL:  ...` and `HINT:  ...` lines
(two spaces after the colon); LOG is not shown. The run ends with one summary line: `ok
<db>: N files loaded in T` or `FAILED <db>: failed after T ...` when stderr is
not a terminal, `✓`/`✗` and colours on a terminal; `NO_COLOR` removes colours.
T is seconds with two decimals (`1.12s`). A success adds `, N paths ignored`
when discovery left paths out (dot-files count). A failure adds what was
left: ` — nothing committed` in atomic mode, ` (unit U of N failed; C earlier
unit(s) already committed)` in psql mode, and `created database "<db>", left
empty` when this run created it.

Cases: `notices/streaming`, `notices/severity`, `commands/verbose`, `terminal/colors`, `notices/summary-line`, `notices/secrets-redacted`, `execution/pgmi-test-without-tests`, `execution/setup-psql-fixture`.

### The --json record

One JSON object on stdout at the end of every run, success or failure, with the
keys and meanings in `docs/CLI.md` "--json output" (`schemaVersion` 1). The
parsed value is the contract, not the bytes: Go escapes `<`, `>`, `&` as
`\u003c` and so on, and indents with two spaces. Neither the record nor the
MCP answer carries the connection password in `error`, `detail`, `hint` or
`where`: Go hides password-shaped text (`password=...`, `PASSWORD '...'`, a
URI's password), the Rust binary replaces the connection's password,
`sslpassword` and cloud token with `***`. Notices are recorded as the server
sent them, in both. A run that stops before it reaches the deploy (a
mistyped project path, a connection that cannot be resolved) has
`durationMs`, `created`, `recreated`, `filesLoaded`, `pathsIgnored` and
`testMacros` null; once it reaches the deploy they are numbers and booleans,
zero and false until known.

Cases: `deploy/json-success`, `flags/compat-record`, `deploy/json-failure`, `errors/attribution`, `errors/diagnostics-secrets`, `execution/atomic-head`, `config/dotenv`. The Rust redaction: unit test `a_failure_record_redacts_detail_hint_and_where`.

### Error attribution

A failure names where it happened: the project file whose text was executing
(by its exact text, or a `Failed in <path>.sql` message), else `deploy.sql`,
and in `--json` the `sqlstate`, `failedFile` and the other diagnostics listed
in `docs/CLI.md`. stderr follows the error with `DETAIL:`, `HINT:` and `WHERE:`
lines when the server sent them, then `LOCATION: <file> line L, column C`, the
line as `LINE L: ...` and a caret under column C. The line and column (counted
in characters) come from the error position: in `deploy.sql`, on the file's
own lines whatever the macro expansion or the head/tail split did (a line
inside a `pgmi_test()` expansion is the macro's line, column 0); inside an
executed file, for a parse error, a function body included. A runtime error has
a line, column 0, when raised in a DO block that opens a file (after any
comment header), the only DO block of the atomic head, or a tail statement (its
first line, or the DO block's line). `WHERE` names a project file (`SQL
statement in ./x.sql`) instead of quoting its text, and identical files are all
named. `phase` is `test` when the file lies under `__test__/` or `__tests__/`.
A `PASSWORD '...'` literal in these lines is shown as `[redacted]`. A login
without the TEMPORARY privilege is told the `GRANT` it needs.

Cases: `errors/attribution`, `errors/head-syntax`, `errors/temporary-privilege`, `errors/line-and-column`, `errors/parameter-value-not-in-statement-echo`.

### Timeout, cancellation, interruption

`--timeout` (default 3m, Go duration syntax) cancels the running statement on
the server, gives the server 2 s to report the cancel, and is exit 16; running out while connecting is exit 11. SIGINT or
SIGTERM cancels and is exit 130, saying `pgmi: interrupted, cancelling
deployment... (again to quit at once)`; a second SIGINT ends at once, still
130. A stopped statement keeps the server's report: the error is `the deploy
exceeded --timeout <d>; raise it, or pass --timeout 0 for no limit` on a
timeout and the server's 57014 on a signal, and `failedFile`, `phase`,
`sqlstate`, `where` and the WHERE and LOCATION lines are those of the
cancelled statement; the summary line says nothing was committed.
Cancel works over TLS and over direct TLS negotiation. A timeout longer than a
Go duration holds (about 2562047h) is refused: exit 2 on the flag, 10 in
`pgmi.yaml` and in the MCP deploy tool. The ROLLBACK of a deploy.sql that
ended inside an open transaction, and the lock release after every deploy,
get 3 s; a server that does not answer by then has its connection closed,
which rolls back as well (Rust integration test
`a_rollback_the_server_never_answers_does_not_hang_the_deploy`).

Cases: `config/pgmi-yaml-timeout-too-long`, `exit-codes/16-timeout`, `exit-codes/130-interrupted`, `exit-codes/130-sigterm`, `flags/timeout-forms`, `cancel/timeout-cancels`, `cancel/timeout-over-tls`, `cancel/sigint-twice`, `env/pgconnect-timeout`, `cancel/ctrl-c-json-exit-code`, `cancel/timeout-leaves-invalid-concurrent-index`, `cancel/stopped-record`.

### Checksums

`checksum` is the hex SHA-256 of the file's bytes after the BOM is stripped.
`pgmi_checksum` is the hex SHA-256 of the normalised text: `--` comments and
nested `/* */` comments become one space; outside literals, every run of ASCII
whitespace (space, tab, CR, LF, VT, FF) becomes one space; outside literals,
ASCII letters are lower-cased (other bytes are kept); single-quoted strings
(with `''` and, after a standalone `E`/`e`, backslash escapes), double-quoted
identifiers (with `""`) and dollar-quoted strings (`$tag$`, the tag starting
with a letter or `_` and not continuing an identifier) are copied unchanged;
leading and trailing Unicode white space is trimmed (the Unicode White_Space
set, so U+00A0, U+0085, U+2028 and U+3000 go; U+200B and U+FEFF stay; a
no-break space inside the text is kept). The values are byte-exact: a database
deployed by one binary must redeploy with another without any run-once file
reading as edited.

Cases: `checksums/golden`, `checksums/cross-binary-redeploy`, `checksums/cross-binary-redeploy-advanced`.

### File formats

`.env` and `--params-file` share one syntax (`KEY=VALUE`, `export`, comments,
single quotes literal, double quotes with escapes and multiple lines). `.env`
expands `$NAME` and `${NAME}` in unquoted and double-quoted values from the
lines above only (a name set only in the environment, or nowhere, expands to
empty); a `.env` that does not parse applies nothing and prints a WARNING
naming the file, never its text. `.env` values enter the process environment,
so they reach the cloud sign-in too; a params
file additionally refuses, with exit 10, a backslash or `$` inside double quotes
and an unquoted ` #`, which `.env` readers disagree on. `pgmi.yaml` values
become text: `~` is empty, numbers and booleans as written (`5432.0` stays
`5432.0`), block scalars keep their final newline.

Cases: `formats/dotenv-edges`, `formats/dotenv-expansion`, `cloud/dotenv-feeds-sdk`, `formats/params-file-edges`, `formats/pgmi-yaml-edges`, `config/pgmi-yaml-invalid`.

### Terminal behaviour

Wizards and prompts run only on a terminal and never when `CI` is set or
`PGMI_NON_INTERACTIVE=1`. Long output (`pgmi ai`) goes through `PGMI_PAGER`,
else `PAGER`, else `less -R`, on a terminal only; `PGMI_PAGER=cat` prints
directly. The pager command is split on white space and run without a shell
(no quoting, no `$` expansion); a pager that cannot start means the output is
printed directly. Wizards and the pager need stdin and stdout on a terminal,
prompts stdin and stderr. Outside the wizards pgmi never sends the terminal a
query (background colour, cursor position, device attributes), so a terminal
that never answers cannot stall it.

Cases: `terminal/init-wizard`, `terminal/non-interactive`, `terminal/pager`, `terminal/colors`, `terminal/pager-command`, `terminal/no-terminal-queries`.

### Cloud sign-in

`--azure`: without a client ID, the default Azure credential chain: managed
identity, Azure CLI, Azure Developer CLI, Azure PowerShell (`pwsh`), in that
order (managed identity on App Service, Cloud Shell, Azure ML, Service Fabric
and Azure Arc as their endpoint variables say, else IMDS), with `AZURE_TOKEN_CREDENTIALS` naming one of them to use alone; with `--azure-client-id`, workload identity then managed
identity; with a tenant ID, a client ID and `AZURE_CLIENT_SECRET` all set, a
service principal (Rust also accepts a certificate, see "Azure service
principal with a certificate"). The token is asked for the Azure Database for PostgreSQL resource and sent as the password.
Without an sslmode, Azure defaults to `verify-full`; AWS to `require`, or
`verify-full` when a root certificate is given. A refused token is exit 11
naming the user and pointing at `--username`.
`--aws`: an RDS IAM token built locally from the AWS credentials
(`host:port?Action=connect&DBUser=...&X-Amz-...`, valid 900 s), region from
`--aws-region`, else `AWS_REGION`, else `AWS_DEFAULT_REGION`, else exit 10.
`--google`: needs `--google-instance` (exit 10); connects through the Cloud SQL
connector with IAM authentication, signed in with Application Default
Credentials: `GOOGLE_APPLICATION_CREDENTIALS`, else gcloud's
`application_default_credentials.json` under `%APPDATA%\gcloud` on Windows
and `~/.config/gcloud` elsewhere, else the metadata server. Lakebase in Go: a
token passed as the password, no separate path; the Rust binary adds
`--databricks` (see "Databricks Lakebase sign-in"). Only one of `--azure`,
`--aws`, `--google` and `--databricks` may be given (exit 10; the message
lists `--databricks` only when it was one of them). `pgmi.yaml`'s
`azure_tenant_id`, `azure_client_id` and `aws_region` are only defaults: the
flag wins, then the environment (`AZURE_TENANT_ID`, `AZURE_CLIENT_ID`;
`AWS_REGION`, then `AWS_DEFAULT_REGION`), then the yaml value. Real-cloud verification: Azure only (owner decision
2026-11-10); AWS and Google are checked against local fakes. Observed on a
real Flexible Server (PGMI-845, 2026-11-11, Go): the Azure CLI, a service
principal with a secret, system- and user-assigned managed identity, and
workload identity each deploy with `verify-full` and no `sslrootcert`, the
server's roots coming from the system trust store; `--azure-tenant-id` wins
over a wrong `AZURE_TENANT_ID`; a principal without a database role is exit 11
naming the user (`check that the signed-in identity has a database role and
that --username matches it`); a wrong tenant is exit 11 (`AADSTS90002`).

Cases: `cloud/azure-cli`, `cloud/azure-managed-identity`, `cloud/azure-powershell`, `cloud/azure-cloud-shell`, `cloud/azure-ml`, `cloud/azure-service-fabric`, `cloud/aws-iam-token`, `cloud/aws-credential-source`, `cloud/dotenv-feeds-sdk`, `cloud/token-auth-requires-tls`, `cloud/google-arguments`, `cloud/azure-client-id-never-uses-the-cli`, `cloud/aws-session-token-and-profile`, `cloud/cloud-ids-env-beats-yaml`.
Service principal and the real Cloud SQL connector: no case; neither can be
pointed at a local fake from the Go binary (see `blackbox/COVERAGE.md` "Cloud
sign-in"); the Rust dialer is checked against the runner's fake Admin API by
a Rust integration test. The
default sslmodes: no case; the fakes need an explicit sslmode to be reached.

## Intended changes

### Google sign-in failure is exit 11

Go exits 1 when Application Default Credentials are missing; Azure and AWS
sign-in failures are 11. The Rust binary uses 11. Flip the expected code in the
case when the Rust binary lands.

Cases: `cloud/google-arguments`.

### Every address's error is reported

When a host resolves to several addresses, Go reports only the last one's
error, so a certificate failure on 127.0.0.1 can read as "connection refused"
from `::1`. The Rust binary reports each address tried.

Cases: `tls/system-roots`.

### POSIX character classes in .pgmiignore

Go refuses `[[:space:]]` and similar as an invalid pattern (exit 10); git
accepts them. The Rust binary accepts them with git's meaning; the case checks
that `sp[[:space:]]ace.sql` hides `sp ace.sql` and keeps `spxace.sql`.

Cases: `dialect/pgmiignore-posix-class`.

### metadata scaffold stops at the repository root

`pgmi metadata scaffold` stops its search for the project root at the first
directory containing `.git`, and prints `Project root: <path>` on stderr when
the root differs from the named directory (PGMI-840).
No case yet: Go walks past `.git`, so a case cannot pass on both binaries
before the switch; the Rust unit test `root_search_stops_at_git` pins it.

### Azure service principal with a certificate

Go's certificate login is unreachable: it needs `AZURE_CLIENT_ID`, and setting
it routes to workload and managed identity only. The Rust binary supports it
(owner, 2026-11-10): with a tenant ID, a client ID and
`AZURE_CLIENT_CERTIFICATE_PATH` (PEM or PKCS#12, optional
`AZURE_CLIENT_CERTIFICATE_PASSWORD`), it signs in as that service principal.
When both a secret and a certificate are set, the secret wins.
With a certificate and a client ID set, Go goes to managed identity and, off
Azure, fails with exit 11 after the IMDS probe gives up (about two minutes).
No case: instance discovery always calls `login.microsoftonline.com`; the
Rust binary signed in with PEM and PKCS#12 against real Azure (PGMI-845,
2026-11-11).

### sslrootcert=system refuses a weaker sslmode

Owner-decided 2026-11-11.

Go (pgx) turns any sslmode into `verify-full` when `sslrootcert=system`, even
an explicit `disable` or `require`. Without an sslmode both binaries use
`verify-full`. With an explicit weaker sslmode the Rust binary refuses with
exit 10 (`weak sslmode "require" may not be used with sslrootcert=system`), as
libpq does.

Cases: `tls/system-roots` (the default). The refusal: Rust unit test
`system_roots_mean_verify_full`.

### Connection-string errors never echo the string

Owner-decided 2026-11-11.

Go hides a URI that does not parse, but echoes a stray keyword
(`missing "=" after "junk"`), an ADO.NET part and a port value. The Rust
binary never quotes connection-string input in an error, on stderr, in
`--json` or in the MCP answer, and redacts the password and `sslpassword` from
every later refusal of the resolved connection.

Cases: `errors/connection-string-secrets` (the forms Go hides too); Rust unit
tests `errors_never_echo_the_input`, `errors_redact_the_password`.

### An unreadable .pgmiignore or a file name that is not UTF-8 is exit 10

Owner-decided 2026-11-11.

A `.pgmiignore` that exists but cannot be read (a directory, no permission) is
exit 1 in Go; the Rust binary refuses it with exit 10 naming the file. Bytes
that are not UTF-8 inside it are read leniently by both. A file whose name is
not UTF-8 is loaded by Go and fails the deploy with exit 13 (SQLSTATE 22021)
after the target database was created; the Rust binary refuses it before
connecting with exit 10 naming the path (invalid bytes shown as U+FFFD),
unless the name starts with `.` or `.pgmiignore` hides it.

Cases: `dialect/pgmiignore-latin1` (lenient reading); Rust unit tests
`pgmiignore_is_read_lossily_and_read_errors_refuse`,
`non_utf8_file_name_is_refused_unless_hidden`.

### TLS keys the driver ignored are honoured or refused

Go sends `ssl_min_protocol_version`, `ssl_max_protocol_version`, `sslcrl`,
`sslcrldir` and `requirepeer` to the server as settings, which refuses them
(exit 11). The Rust binary honours `ssl_min_protocol_version` and
`ssl_max_protocol_version` (TLSv1.2 and TLSv1.3; a range with neither, or an
unknown value, is exit 10), `sslsni=0` (no SNI) and `sslcertmode=disable` (no
client certificate is read or sent), and refuses with exit 10 before
connecting `sslcrl`, `sslcrldir`, `requirepeer` and `sslcertmode=require`,
which it cannot check. This extends "Unknown connection-string keys are
refused before connecting". Owner-decided 2026-11-11 for the refusals.

Cases: none (Go fails these on the server); Rust unit tests
`tls_keys_are_read_or_refused`, `refusals` (parse), `versions_and_sni_reach_the_config`.

### Client certificate files

Both binaries refuse an `sslcert` without an `sslkey`, or the reverse, with
exit 10 under a TLS sslmode, use `~/.postgresql/postgresql.crt` and `.key` only
when both exist, and read neither under `sslmode=disable`. The Rust binary
also refuses, with exit 10, a private key file that group or others can read
(0600 or less, 0640 when owned by root), as libpq does; Go does not check.
The permission check is owner-decided 2026-11-11.

Cases: `tls/client-certificate-pairs`; the permission check: Rust unit test
`a_key_others_can_read_is_refused`.

### Windows file locations

On Windows the Rust binary reads `%APPDATA%\postgresql\pgpass.conf`,
`root.crt`, `postgresql.crt` and `postgresql.key`, as libpq does (Go: pgx does
the same), and `pgmi config` saves the password there; the home directory is
`HOME`, else `USERPROFILE`.

Cases: none (the suite runs on Linux); Rust unit test
`postgres_files_follow_the_platform`.

### serve refuses a request id already in flight

Owner-decided 2026-11-11.

Go accepts a second `tools/call` whose id matches one still running: both run,
the first loses its cancellation, and the client gets two answers with one
id. The Rust binary answers the second at once with `-32600` (`request id 7
is already in flight`) and leaves the first running.

Go matches ids by their spelling, so `"\u006aob"` neither cancels nor
duplicates a call whose id is `"job"`. The Rust binary matches the decoded
value (PGMI-868): a string id by its text, a number id as written, and a
string never matches a number (`"1"` is not `1`).

Cases: none (Go's answer is not deterministic); Rust unit tests
`a_request_id_already_in_flight_is_refused`,
`request_ids_match_by_value_not_by_spelling`.

### serve bounds what a client can make it hold

Owner-decided 2026-11-11 (PGMI-868).

Go reads a line of any length, runs any number of tool calls at once and
queues any number of answers, so a client can grow serve until it runs out
of memory. The Rust binary bounds all three:

- A line over 16 MiB (eight times the 2 MB the cases read) is answered at
  once, while it is still arriving, with `-32600` and a null id (`a message
  may hold at most 16 MiB`); its bytes are discarded up to the next newline
  and the session goes on.
- With 32 tool calls running, another `tools/call` is answered at once with
  `-32000` (`32 tool calls are already running; wait for one to finish`).
  `ping`, `tools/list` and `notifications/cancelled` are still served.
- When 1024 answers are waiting because the client does not read them, the
  session ends: running calls are cancelled and serve exits as at the end
  of input.

Cases: none (Go accepts all three); Rust unit tests
`a_line_over_16_mib_is_refused_at_once_and_the_session_goes_on`,
`calls_beyond_32_are_refused_and_cancels_still_get_through`,
`a_client_that_never_reads_is_dropped_once_its_answers_pile_up`,
`a_line_over_the_limit_is_dropped_and_counted_once`.

### Two metadata elements in one comment are refused

Owner-decided 2026-11-11 (PGMI-868).

Go reads the first `<pgmi-meta>` element of a comment and ignores the rest,
so a second element with another id or other sort keys in the same `/* */`
is silently dropped. The Rust binary refuses it as it refuses two blocks in
separate comments: exit 10, `Multiple metadata blocks found`, naming the
file. A second element that is not even well-formed counts too.

Cases: none (Go accepts it); Rust unit test
`two_elements_in_one_comment_are_refused`.

### Known secrets are shown as ***

Owner-decided 2026-11-11 (PGMI-868).

Go prints what the server sends. The Rust binary replaces the exact value of
each secret it knows with `***` before anything leaves it: the connection
password, `sslpassword`, and every sign-in token (Azure, AWS, Google,
Databricks, renewed ones included). This covers notices (message, DETAIL,
HINT) on stderr, in `--json` and in serve answers, and errors on the same
three paths. Values shorter than three bytes are left alone, so a one-letter
password does not blank out every letter. No pattern is guessed. A password
read from a password file (`~/.pgpass`, `PGPASSFILE`, `passfile`) is known
from the moment it is read, for the maintenance and the target database.

Cases: `notices/secrets-redacted`, `notices/pgpass-secret-redacted`.

### A non-ASCII letter glued to CALL belongs to the word

Owner-decided 2026-11-11 (PGMI-863).

PostgreSQL's lexer reads any byte from 0x80 up as part of an identifier
(`scan.l`: `ident_start [A-Za-z\200-\377_]`; "letters with diacritical marks
and non-Latin letters", SQL Syntax, Identifiers and Key Words). So `ÄCALL
pgmi_test()` is the identifier `ÄCALL`, not a CALL. The Rust binary leaves it
to the server, which names `ÄCALL` in its syntax error; Go expanded the macro.
A no-break space glued to CALL is the same case. Only ASCII white space
separates the macro's tokens; a vertical tab does not, which matches
PostgreSQL 15 (16 and later treat it as white space).

Cases: `dialect/call-glued-to-a-non-ascii-letter`; Rust unit test
`a_non_ascii_character_glued_to_call_is_part_of_the_word`.

### -d POSTGRES is not the maintenance database

Owner-decided 2026-11-11 (PGMI-863).

`--overwrite` refuses to drop the maintenance database. Go compared the names
ignoring case, so `-d POSTGRES --overwrite` was refused. Database names are
case-sensitive in PostgreSQL, and the Rust binary compares them exactly: only
`postgres` (or the configured maintenance database) is protected.

Cases: none (needs a second database named in capitals on the shared
server); Rust unit test
`the_maintenance_database_is_never_the_overwrite_target`.

### Wizard lists wrap around

Owner-decided 2026-11-11 (PGMI-863).

In the `init` and `config` wizards, the selection lists (providers,
templates) wrap from the last entry to the first and back. Go stopped at the
ends.

Cases: none; Rust unit tests `moves_and_picks` (the shared list) and
`the_provider_list_wraps_at_both_ends`.

### Nothing that cannot be reached is carried over

Go's Azure username/password login (resource-owner password) is unreachable
for the same reason, fails under multi-factor sign-in, and is dropped (owner,
2026-11-10). Go's Cloud SQL connector sends metrics to Cloud Monitoring by
default, and its docs claim a `schema.xsd` that nothing validates against. The
Rust binary has no username/password Entra login, sends no metrics and
validates no XSD; the docs line is fixed. PostgreSQL password authentication
is unaffected.
No case: none of these is observable from outside without a real cloud.

## Decided changes (owner, 2026-11-10)

Each was an open question in the first draft; the owner accepted the libpq
behaviour in every case. Each case keeps Go's expectation and states the Rust
one under `expect.rust`.

### sslmode=allow retries with TLS

Go (pgx) never retries `allow` with TLS, so a server that requires TLS refuses
it. The Rust binary tries plaintext first and, when the server refuses it,
retries with TLS, as libpq does.

Cases: `tls/tls-only-and-plain-servers`.

### Unknown connection-string keys are refused before connecting

Go sends an unknown key to the server as a setting, which refuses it (exit 11,
`unrecognized configuration parameter`). The Rust binary refuses it before
connecting with exit 10, naming the key, as libpq does. The three keys
`docs/CONNECTIONS.md` documents for single-session servers
(`default_query_exec_mode`, `statement_cache_capacity`,
`description_cache_capacity`) keep their documented meaning.

Cases: `connection/keywords`.

### PGPORT may list several ports

Go refuses `PGPORT=5432,5433` (exit 10). The Rust binary accepts a list matching
`PGHOST`, as libpq does. It also accepts spaces around a port
(`PGPORT=" 5432 "`), as libpq does, where Go refuses it (owner, 2026-11-11;
Rust unit test `pgport_edge_cases`).

Cases: `connection/target-session-attrs`.

### No password available

Go sends no password and reports the server's `password authentication failed`.
The Rust binary does what libpq does: when the server asks for a password and
none was found, it stops and reports `no password supplied` naming where it
looked (the connection string, `PGPASSWORD`, `~/.pgpass`), exit 11. Trust, peer
and certificate logins need no password and are unaffected.

Cases: `connection/pgpass-world-readable`.

### Encrypted legacy client keys and require_auth are kept

Both work in Go and both stay: the Rust binary decrypts OpenSSL's legacy
encrypted keys with `sslpassword` and honours `require_auth`, so no working
setup breaks at the switch.

Cases: `tls/encrypted-client-key`, `tls/require-auth-and-version`.

### Encrypted PKCS#8 client keys open

Go cannot parse an encrypted PKCS#8 key (`ENCRYPTED PRIVATE KEY`, what
`openssl pkcs8 -topk8` writes) and refuses it with exit 10 (`failed to parse
private key`), with or without a password. The Rust binary opens it with
`sslpassword`/`PGSSLPASSWORD`, as libpq does: PBES2 (PBKDF2 or scrypt with
AES-CBC or DES-EDE3-CBC) and PKCS#12 PBE-SHA1-3DES (`-v1 PBE-SHA1-3DES`). A
wrong password is exit 10 `decryption password incorrect`; a missing one is
exit 10 asking for `sslpassword` or `PGSSLPASSWORD`.

Cases: `tls/encrypted-pkcs8-client-key`.

### serve answers a bare null and never echoes invalid bytes (owner, 2026-11-11)

Go ignores a line holding only `null`; JSON-RPC 2.0 calls it an invalid
request. The Rust binary answers it with `-32600` and a null id. Go echoes an
id holding invalid UTF-8 as raw bytes, so its answer is not valid JSON; the
Rust binary reads each line lossily and echoes U+FFFD, so every answer is
valid JSON (questions Q1 and Q2 in `rust/docs/decisions/go-review.md`).

Cases: `serve/null-request`; Q2 by the Rust unit tests of `serve::wire`.

### A minimum protocol above 3.0 is refused for now (owner, 2026-11-11)

Go (pgx) speaks protocol 3.2 when `min_protocol_version=3.2` asks for it. The
Rust driver speaks 3.0 only, so the Rust binary refuses `min_protocol_version`
`3.2` or `latest` before connecting, exit 10 (`protocol 3.2 is not supported
yet`). `max_protocol_version` is honoured: it caps the version, and 3.0 is
always within it. Adding 3.2 to pgmi's own connection layer is PGMI-865, done
only if a user asks (question Q4 in `rust/docs/decisions/go-review.md`).

Cases: `connection/protocol-version`.

### Three smaller differences (owner, 2026-11-11)

After the user confirms replacing a `pgmi.yaml` it cannot read as settings, the Rust
binary writes a fresh connection block; Go edits the file in place. An
`AZURE_TOKEN_CREDENTIALS` value that names no credential is refused before
connecting with exit 10 (Go: 1). Both wizards draw full-screen, as Go's do.

Cases: `cloud/azure-token-credentials-invalid` (the exit code). The fresh
block: `terminal/config-replace-unparseable` checks what both binaries keep;
the replacement itself is recorded in `rust/docs/decisions/0013-yaml.md`.

### Every accepted connection key is honoured or refused (owner, 2026-11-11)

No connection setting is accepted and then ignored (PGMI-860; libpq keys,
question Q3 in `rust/docs/decisions/go-review.md`). Go sends most of the keys
below to the server as settings, which refuses them (exit 11). The Rust
binary handles each one as libpq does, or refuses it before connecting with
exit 10 when it needs something pgmi has not:

- `keepalives`, `keepalives_idle`, `keepalives_interval`, `keepalives_count`:
  set on the socket before the startup message. Keepalive is on by default
  with Go's timings (first probe after 15 s idle, then every 15 s, 9 probes);
  `keepalives=0` turns it off, and 0 for the others keeps the default. On
  Windows the count needs Windows 10 1703 or later. A value that is not a
  whole number is exit 10.
- `tcp_user_timeout` (milliseconds): sent to the server as its setting, as Go
  does, and also set on pgmi's own socket on Linux, the only release target
  with the option. A bad value is exit 10 (Go: 11, from the server).
- `load_balance_hosts`: `disable` (default) or `random`, which tries the hosts,
  and each host's addresses, in random order.
- `hostaddr`: a numeric address per host (empty entries look the host up);
  pgmi connects to it without a lookup and keeps the host name for TLS,
  `.pgpass` and messages. Go refuses it (keyword form, exit 10) or sends it to
  the server (URI, exit 11). `PGHOSTADDR` stays ignored, as in Go.
- GSSAPI: neither binary has it. `gssencmode=disable` and `prefer` (what libpq
  without GSSAPI does) and `gssdelegation=0` are honoured; `krbsrvname` is
  accepted, as Go does, and only names the service of a GSSAPI sign-in, which
  a server cannot get from either binary; `gssencmode=require`,
  `gssdelegation=1` and any `gsslib` are exit 10.
- `sslcompression=0` is honoured (pgmi's TLS never compresses);
  `sslcompression=1` is exit 10.
- `client_encoding`: the named encoding is set on the session in place of
  the database's own, in both binaries, so the server converts from it
  (`LATIN1` on a UTF8 database stores UTF-8 file text as LATIN1 characters,
  as in Go). The Rust binary also reads `PGCLIENTENCODING`, as libpq does;
  Go ignores it. `auto` (libpq: from the locale) is exit 10 (Go: 11, from
  the server); an encoding the server does not know is exit 11 in both.
- `default_query_exec_mode`, `statement_cache_capacity`,
  `description_cache_capacity`: checked as pgx does (exit 10 for a bad value,
  in both binaries); pgmi never names prepared statements, so every mode
  works with single-session servers.

The rule is enforced in code: `resolve::finish` removes every key it
handles and refuses a leftover one, and the unit test
`every_accepted_key_is_consumed_or_refused` walks the parser's accepted list.

Cases: `connection/keepalives`, `connection/tcp-user-timeout`, `connection/load-balance-hosts`, `connection/hostaddr`, `connection/gssapi-keys`, `connection/sslcompression`, `connection/client-encoding`, `connection/pgx-options`.

### Databricks Lakebase sign-in (owner, 2026-11-10)

New in the Rust binary; Go has none, so these cases carry `impl: rust` and
are skipped for Go (PGMI-849, record 0023). `--databricks` asks the
Databricks workspace for a Lakebase database credential
(`POST /api/2.0/postgres/credentials` with the endpoint; the answer's `token`
is the password, `expire_time` its end) for the endpoint in
`--databricks-endpoint`, else `PGMI_DATABRICKS_ENDPOINT`
(`projects/<project>/branches/<branch>/endpoints/<endpoint>`). Missing or
malformed: exit 10. `--databricks-endpoint` without `--databricks` is a usage
error (exit 2).

The workspace comes from `DATABRICKS_HOST`, else a `~/.databrickscfg`
profile (`DATABRICKS_CONFIG_PROFILE`, else the file's `default_profile`,
else `DEFAULT`; `DATABRICKS_CONFIG_FILE` moves the file; the file is read
only when a profile is named or the environment sets no host, token or
client). It must be `https` (exit 10). Sign-in, first match: a personal
access token (`DATABRICKS_TOKEN`); OAuth M2M (`DATABRICKS_CLIENT_ID` and
`DATABRICKS_CLIENT_SECRET`, `POST /oidc/v1/token`, scope `all-apis`); else
the Databricks CLI (`databricks auth token --profile <p>` when a profile was
read, else `--host <host>`), then, for an Azure Databricks host, pgmi's Azure
sign-in for the Azure Databricks resource. `DATABRICKS_AUTH_TYPE` (`pat`,
`oauth-m2m`, `databricks-cli`, `azure-cli`, `azure-msi`,
`azure-client-secret`) forces one; another value is exit 10.

The credential is reused for every connection of the deploy until less than
five minutes of it are left, then replaced before the next connection.
Workspace answers: 401, 403 and 404 are exit 11 naming the cause; 429 and
5xx are tried three times, then exit 11. A credential the database refuses
is exit 11 naming Databricks Lakebase, the user and `--username`. Without an
sslmode the default is `verify-full`; a plaintext one is exit 10. No token
reaches stderr, stdout or `--json`.

Cases: `cloud/databricks-pat`, `cloud/databricks-refresh`, `cloud/databricks-m2m`, `cloud/databricks-cli`, `cloud/databricks-azure`, `cloud/databricks-errors`.

## Behaviour changes in 0.20

### Changed exit codes and newly refused input

Every difference above, old (Go, 0.14) against new (Rust, 0.20). "Owner" rows
are decided; the others are intended changes from the first draft. The 0.20
release notes take their "Behaviour changes" section from this table.

| Change | 0.14 (Go) | 0.20 (Rust) | Decided |
|---|---|---|---|
| Google sign-in without Application Default Credentials | exit 1 | exit 11 | intended |
| A host with several addresses fails to connect | only the last address's error | every address's error; exit 11 both | intended |
| POSIX class such as `[[:space:]]` in `.pgmiignore` | refused, exit 10 | accepted | intended |
| `metadata scaffold` project-root search | walks past `.git` | stops at `.git`, prints `Project root:` | intended |
| Azure service principal with a certificate | not reachable | signs in | owner 2026-11-10 |
| `sslrootcert=system` with an explicit weaker sslmode | upgraded to `verify-full` | newly refused, exit 10 | owner 2026-11-11 |
| Connection-string parse errors | echo a keyword, ADO.NET part or port | never quote the input; exit codes unchanged | owner 2026-11-11 |
| `.pgmiignore` that exists but cannot be read | exit 1 | exit 10 | owner 2026-11-11 |
| Project file whose name is not UTF-8 | exit 13, after the target database was created | newly refused before connecting, exit 10 | owner 2026-11-11 |
| `sslcrl`, `sslcrldir`, `requirepeer`, `sslcertmode=require` | exit 11 (the server refuses the setting) | newly refused before connecting, exit 10 | owner 2026-11-11 |
| `ssl_min_protocol_version`, `ssl_max_protocol_version`, `sslsni=0`, `sslcertmode=disable` | exit 11 (the server refuses the setting) | honoured; an unknown version or an empty range exit 10 | intended |
| Client private key that group or others can read | used | newly refused, exit 10 | owner 2026-11-11 |
| Windows: files under `%APPDATA%\postgresql` | read, and `pgmi config` saves the password there | unchanged | no change |
| `min_protocol_version=3.2` or `latest` | protocol 3.2 | newly refused before connecting, exit 10 (known gap, PGMI-865) | owner 2026-11-11 |
| Two `<pgmi-meta>` elements in one comment | the first used, the rest ignored | newly refused, exit 10 | owner 2026-11-11 |
| `serve`: a line over 16 MiB | read | newly refused with `-32600`, session goes on | owner 2026-11-11 |
| `serve`: a `tools/call` while 32 calls run | runs | newly refused with `-32000` | owner 2026-11-11 |
| `serve`: 1024 answers the client has not read | queued | the session ends | owner 2026-11-11 |
| `serve`: a line holding only `null` | no answer | `-32600` with a null id | owner 2026-11-11 |
| `serve`: an id holding invalid UTF-8 | echoed as raw bytes (invalid JSON) | echoed with U+FFFD | owner 2026-11-11 |
| `serve`: a `tools/call` whose id is still running | both run, two answers | second refused with `-32600` | owner 2026-11-11 |
| `serve`: a cancel or a repeated id spelled with escapes (`"\u006aob"` for `"job"`) | missed | matched by the decoded string | owner 2026-11-11 |
| Azure username/password (resource-owner) login | not reachable | removed | owner 2026-11-10 |
| `pgmi init`: `sslmode` in the scaffolded `pgmi.yaml` | `prefer`, refused with token sign-in | not written; the default applies (`prefer`, or TLS only with token sign-in) | owner 2026-11-11 |
| Token sign-in refusing `sslmode: prefer` from `pgmi.yaml` | names the source | names the file line and the fix; exit 10 both | owner 2026-11-11 |
| Basic template's privilege test | fails on any PUBLIC-executable function in `public`, also a hosting provider's | checks only the deploying role's functions | owner 2026-11-11 |
| Known secrets (password, `sslpassword`, sign-in tokens) in notices, errors, `--json`, serve | printed | shown as `***` | owner 2026-11-11 |
| A non-ASCII letter glued to `CALL` (`ÄCALL pgmi_test()`) | expanded | left to the server, as PostgreSQL reads one identifier | owner 2026-11-11 |
| `--overwrite -d POSTGRES` with maintenance database `postgres` | refused (case-insensitive) | allowed: names compared exactly | owner 2026-11-11 |
| Wizard selection lists | stop at the ends | wrap around | owner 2026-11-11 |
| Advanced template: a login without `CREATEDB`/`CREATEROLE`, or extensions Azure has not allow-listed | fails partway, exit 13 | named before anything changes, exit 13 | owner 2026-11-11 |
| Advanced template on Lakebase without `provider_role_pattern`/`provider_schemas` | refused by the guards, exit 13 | detected; deploys | owner 2026-11-11 |
| Google Cloud SQL: a transient connection failure | not retried | retried with backoff, as every other route | intended |
| Google Cloud SQL dialer | Google's Go connector | pgmi's own dialer (record 0009): TLS by the dialer, inner `sslmode=disable`, IAM login, as before | owner 2026-11-10 |
| `--version` / `version` | Go build info | the same two lines (and the repository line); `Commit`/`Built` from the release build, else `unknown` | intended |
| Databricks Lakebase sign-in (`--databricks`, `--databricks-endpoint`) | not available | a credential from the workspace, refreshed before it expires | owner 2026-11-10 |
| Cloud SQL connector metrics to Cloud Monitoring | sent | none | owner 2026-11-10 |
| `sslmode=allow` against a server that requires TLS | exit 11 | retries with TLS, connects | owner 2026-11-10 |
| Unknown connection-string key | exit 11 (the server refuses the setting) | newly refused before connecting, exit 10 | owner 2026-11-10 |
| `PGPORT` listing several ports | refused, exit 10 | accepted, one per `PGHOST` entry | owner 2026-11-10 |
| `PGPORT` with spaces around the number (`" 5432 "`) | refused, exit 10 | accepted, as libpq does | owner 2026-11-11 |
| `keepalives*`, `load_balance_hosts`, `gssencmode=disable`/`prefer`, `gssdelegation=0`, `sslcompression=0` | exit 11 (the server refuses the setting) | honoured | owner 2026-11-11 |
| `hostaddr` | exit 10 (keyword form) or 11 (URI) | honoured, no lookup | owner 2026-11-11 |
| `gssencmode=require`, `gssdelegation=1`, `gsslib`, `sslcompression=1` | exit 11 (the server refuses the setting) | newly refused before connecting, exit 10 | owner 2026-11-11 |
| A bad `tcp_user_timeout`, `keepalives*` or `load_balance_hosts` value | exit 11 | exit 10 | owner 2026-11-11 |
| `PGCLIENTENCODING` | ignored | sets the session's `client_encoding`, as libpq does | owner 2026-11-11 |
| `client_encoding=auto` | exit 11 (the server refuses the value) | refused before connecting, exit 10 | owner 2026-11-11 |
| Server asks for a password and none was found | exit 11, the server's `password authentication failed` | exit 11, `no password supplied` naming where it looked | owner 2026-11-10 |
| Encrypted legacy client keys, `require_auth` | work | work (kept) | owner 2026-11-10 |
| Encrypted PKCS#8 client key (PBES2 or PBE-SHA1-3DES) | refused, exit 10 | opens with `sslpassword` | owner 2026-11-10 |
| Windows: Google ADC file `%APPDATA%\gcloud\application_default_credentials.json` | read | read | no change |
| Confirmed replace of a `pgmi.yaml` that cannot be read as settings | edited in place | fresh connection block | owner 2026-11-11 |
| `AZURE_TOKEN_CREDENTIALS` naming no credential | exit 1 | exit 10 | owner 2026-11-11 |
| Wizards | full-screen | full-screen (kept) | owner 2026-11-11 |
| TLS or cloud sign-in on an x86-64 CPU without AES-NI, PCLMULQDQ, SSSE3, AVX, AVX2, BMI1, BMI2, ADX (before about 2014), or an arm64 CPU without NEON, AES, PMULL, SHA2 | works | newly refused before connecting, exit 10, naming the missing features; `sslmode=disable` and commands without a database still run (record 0021) | owner 2026-11-11 |

No case of its own: each row's section above names its cases or its Rust unit
test.
