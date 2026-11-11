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
the Rust binary differs from Go on purpose.

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

Cases: `commands/usage-errors`, `exit-codes/0-success`, `exit-codes/2-usage`, `exit-codes/10-invalid-config`, `exit-codes/11-connection-failed`, `exit-codes/12-overwrite-denied`, `exit-codes/13-sql-failed`, `exit-codes/14-no-deploy-sql`, `exit-codes/15-concurrent-deploy`, `exit-codes/16-timeout`, `exit-codes/130-interrupted`, `exit-codes/130-sigterm`, `commands/init-unwritable`.
Exit 3: no case; a release binary cannot panic on purpose. The Rust build proves it with its `testhooks` feature (`rust/pgmi/tests/panic.rs`): a panic exits 3 with the `--json` record, and a panicking serve tool answers `isError` with exit code 3.

## Commands

### info

`pgmi info [path]` summarises a project without a database: template, whether
`deploy.sql` and `pgmi.yaml` exist, file counts, directories; `--json` gives
the same as an object. A missing path is exit 10.

Cases: `commands/info`.

### init

`pgmi init [path] [-t minimal|basic|advanced]` writes a template. Without `-t`
and without a terminal it writes `minimal`; on a terminal it runs the wizard. A
file named `<name>.scaffold` is written as `<name>`. A target holding anything
but `pgmi.yaml`, `.env` or `.git` is exit 2; an unknown template is exit 2.
`{{PROJECT_NAME}}` in a template becomes the target folder's name, as is in
text files and JSON-escaped in `.json` files. Answering Yes to "Set up the
connection now?" runs the `config` wizard and writes its settings into the
scaffolded `pgmi.yaml`.

Cases: `commands/init`, `commands/init-unwritable`, `terminal/init-wizard`, `terminal/non-interactive`, `commands/init-project-name`, `terminal/init-wizard-connection`.

### Template deployments

The basic and advanced templates, as `init` writes them, deploy into a new
database with `--param env=dev` and redeploy over it without error. The
templates themselves are shared files (`shared/templates/`), so this checks the
binary's half: loading, the session API, the execution contract and the test
macro under real projects.

Cases: `deploy/basic-template-converges`, `deploy/advanced-template-converges`, `checksums/cross-binary-redeploy`, `checksums/cross-binary-redeploy-advanced`.

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
or description, an unknown entity, unclosed XML, two blocks, a block over
10240 bytes. A file without a block gets the id `md5("./<path>")` read as a
uuid, the same in `metadata plan` and `pgmi_plan_view.generic_id`.

Cases: `commands/metadata`, `commands/metadata-invalid`, `commands/metadata-duplicate`, `commands/metadata-scaffold-run-once`, `commands/metadata-forms`, `commands/metadata-refusals`.

### templates, version, ai, completion, help

`templates list|describe`, `version` and `--version` (`pgmi <version> (compat
1)`), `ai` (the overview) and `ai contract` (the session API), `completion
bash|zsh|fish|powershell`, `help <command>` and `--help`. An unknown
subcommand, template or shell, or extra arguments, is exit 2. `pgmi` with no
arguments prints the root help on stdout and exits 0; on a terminal it draws
the banner on stderr instead, unless `PGMI_NO_BANNER` is set.

Cases: `commands/templates`, `commands/version`, `commands/ai`, `commands/completion`, `commands/help`, `commands/bare`.

### Verbose output

`-v` shows `RAISE DEBUG` from `deploy.sql` and lists ignored paths; without it,
DEBUG is hidden and NOTICE, INFO and WARNING are shown. With `-v` every notice
line (DETAIL and HINT included) starts with the seconds since the deploy
started, `[1.23s] `.

Cases: `commands/verbose`, `notices/verbose-elapsed`.

### serve

`pgmi serve` is an MCP server over stdio (JSON-RPC 2.0, one message per line):
`initialize`, `tools/list` with seven tools (`deploy`, `init`, `metadata_plan`,
`metadata_validate`, `templates_list`, `ai_overview`, `ai_contract`),
`tools/call`, `ping`. A malformed line gets -32700 with a null id, an unknown
method -32601, and the session goes on. Calls run concurrently: a ping is
answered during a deploy, and `notifications/cancelled` stops a running deploy,
which rolls back. End of input exits 0. Full reference: `docs/CLI.md`
"pgmi serve".

Malformed input, each answered and the session continuing: text that is not
JSON, including invalid UTF-8 outside a string, is -32700 with a null id;
JSON that is not a request object (`[]`, a number, a string) or has a member
of the wrong type (`"jsonrpc": 2`) is -32600 with a null id; a missing or
wrong `jsonrpc` is -32600 echoing the id; an id that is an object, array,
boolean or null is -32600 with a null id; `tools/call` without params or with
params that are not an object is -32602, an unknown tool -32602; wrong tool
arguments are a tool result with `isError` and the exit code (10 for a
missing path or an unknown argument). Blank lines and notifications get no
answer; a 2 MB line is read; invalid UTF-8 inside a string reads as U+FFFD.
String and number ids (`1.5`) are echoed as sent. Not pinned, owner
questions Q1 and Q2 in `rust/docs/decisions/go-review.md`: the bare value
`null` gets no answer, and an id holding invalid UTF-8 is echoed as raw bytes.

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

Cases: `serve/session`, `serve/deploy-tool`, `serve/cancel`, `serve/malformed`, `serve/protocol-versions`, `serve/deploy-guards`, `serve/deploys-in-sequence`, `serve/concurrent-deploys`, `serve/deploy-timeout`.

### config

`pgmi config [path]` is a terminal wizard: it asks before replacing the
connection settings of an existing `pgmi.yaml`, tests the connection, writes
`pgmi.yaml` keeping the file's comments and other keys, and offers to save the
password to `~/.pgpass`. A `pgmi.yaml` that does not parse is replaced only
after a confirmation. Without a terminal it is exit 2. Declining a prompt or
Esc on the first screen is exit 12; Ctrl-C in the wizard is exit 130.

Cases: `terminal/config-wizard`, `terminal/prompt-cancel`, `terminal/config-refusals`, `terminal/config-replace-unparseable`.

## Deploy

### Project discovery and loading

Every file under the project path enters the session except the built-in
exclusions, dot-files and `.pgmiignore` patterns (`.gitignore` syntax: comments,
globs, classes, `**`, directory patterns, `!`, escapes). A pattern set that
would hide `deploy.sql` is exit 10. A leading UTF-8 BOM is stripped from every
file before anything else sees it. Each file is capped at 10 MiB
(`PGMI_MAX_FILE_SIZE` overrides; it must be a positive whole number of bytes,
else exit 10). Full reference: `docs/DEPLOY-GUIDE.md` "What gets loaded".

The walk never follows a symbolic link: a linked file or directory that
`.pgmiignore` does not hide is exit 10 naming it; `deploy.sql` itself may be
a link. A file containing a NUL byte or invalid UTF-8 is exit 10 naming it
(for `deploy.sql`, with the byte offset). A directory named `__<name>__`
other than `__test__` and `__tests__` is exit 10. A file larger than the cap
is exit 10.

Cases: `dialect/pgmiignore`, `dialect/pgmiignore-hides-deploy`, `execution/loader-at-scale`, `dialect/project-walk`, `commands/metadata-refusals`.

### Session API

The temporary tables, views and functions `deploy.sql` sees are defined by
`shared/session/schema.sql` and `shared/contract/api-v1.sql`; every binary
embeds these files unchanged, so they are the contract. `pgmi ai contract`
prints the same as JSON. `--compat 1` selects this interface version (the only
one); any other value is exit 10. Plan order: sort key, then path, byte order; a file
without metadata sorts by its path; a missing `<description>` is an empty
string. Full reference: `docs/session-api.md`.

Cases: `execution/loader-at-scale`, `params/precedence`, `commands/ai`, `flags/compat`.

### Parameters

Sources, highest first: `--param`, `--params-file` (later files win; `-` reads
stdin, at most once), `PGMI_PARAM_*` variables (the project `.env` is loaded
first and never overrides a variable already set), `pgmi.yaml` `params`. Keys
are lower-cased. Each reaches `deploy.sql` as `current_setting('pgmi.<key>')`
and in `pgmi_parameter_view`. Refused with exit 10: an empty `PGMI_PARAM_*`, a
`--param` without `=`, a missing params file, two keys differing only in case
in one file, a `PGMI_PARAM_` with nothing after the prefix. Full reference:
`docs/CLI.md` "Parameter Flags".

Cases: `params/env-var-reaches-session`, `params/precedence`, `flags/params-file`, `config/precedence`, `config/dotenv`, `exit-codes/10-invalid-config`.

### Connection sources

The connection comes from `--connection`, else `PGMI_CONNECTION_STRING`, else
`DATABASE_URL`, else `-h/-p/-U` with the `PG*` variables, else `pgmi.yaml`,
else defaults (localhost:5432, maintenance database `postgres`). A connection
string may be a URI, libpq keyword/value or ADO.NET. `-d` (or `PGDATABASE`, or
`pgmi.yaml` `database`) names the target; the string's database is the
maintenance database. Refused with exit 10: `--connection` with `-h`, `-p` or
`-U`; `-h`, `-p` or `-U` next to `PGMI_CONNECTION_STRING` or `DATABASE_URL`;
`--connection` with `--sslmode`; `PGSERVICE` or `service=`; an unknown key in
`pgmi.yaml`. Unless typed on the command line, stderr says where the connection
came from (`connection: from <source>`). Full reference: `docs/CLI.md`
"Precedence", `docs/CONFIGURATION.md`.

Cases: `flags/connection-forms`, `flags/granular-flags`, `env/connection-string-vars`, `env/connection-string-vars-refuse-flags`, `env/pg-variables`, `env/pgservice-refused`, `config/pgmi-yaml`, `config/pgmi-yaml-invalid`, `config/precedence`.

### Connection resolution

libpq-style: `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`,
`PGAPPNAME` (default `application_name` is `pgmi`), `PGCONNECT_TIMEOUT`,
`PGPASSFILE`, `PGOPTIONS`, `PGTZ`, `PGTARGETSESSIONATTRS`, `PGCHANNELBINDING`,
`PGREQUIREAUTH`, the `PGSSL*` variables; `PGHOSTADDR` and `PGSSLCRL` are
ignored. `~/.pgpass` (or `PGPASSFILE`) supplies a missing password, wildcards
allowed; a file readable by group or others is ignored with a WARNING. Several
hosts are tried in order and the error names every one tried;
`target_session_attrs` takes `any`, `read-write`, `read-only`, `primary`,
`standby`, `prefer-standby`. `options=` reaches the server. A connection that
fails transiently (SQLSTATE class 08, 53300, 57P03, refused, reset, unreachable,
timeout) is tried 4 times in all, waiting about 0.1 s, 0.2 s, 0.4 s, each line
on stderr; other failures are not retried. `max_protocol_version` and
`min_protocol_version` (`3.0`, `3.2`) are accepted, and a deploy and a cancel
work with both set to `3.2` (Go speaks 3.2 then); `sslsni`, `sslcertmode` and
`client_encoding` are accepted.

Cases: `connection/pgpass`, `connection/pgpass-wildcard-and-file`, `connection/pgpass-world-readable`, `connection/multi-host`, `connection/target-session-attrs`, `connection/keywords`, `connection/unix-socket`, `env/pg-variables`, `env/pgappname`, `env/pgconnect-timeout`, `env/ignored-variables`, `errors/connection-failures`, `exit-codes/11-connection-failed`, `connection/protocol-version`.

### TLS

sslmode `disable`, `allow` (plaintext), `prefer` (TLS when the server offers
it), `require` (no verification, unless a root certificate is given, which makes
it `verify-ca`), `verify-ca` (chain only), `verify-full` (chain and host name;
an IP address needs an IP SAN). The root certificate is `sslrootcert`, else
`~/.postgresql/root.crt`; `sslrootcert=system` uses the platform trust store
(and `SSL_CERT_FILE`). The client certificate is `sslcert`/`sslkey`, else
`~/.postgresql/postgresql.crt`/`.key`; an encrypted key opens with
`sslpassword`, a wrong one is exit 10. `channel_binding=require` needs TLS;
`sslnegotiation=direct` is supported; `require_auth` refuses another method.
`--sslmode` replaces the sslmode of a string from the environment. Token
sign-in (cloud) refuses `disable`, `allow` and `prefer` with exit 10. `--json`
reports `tls.enabled`, `tls.version` (`TLS 1.3`), `tls.sslmode`.

Cases: `tls/sslmode-matrix`, `tls/sslmode-refusals`, `tls/default-files`, `tls/default-files-good-root`, `tls/tls-only-and-plain-servers`, `tls/client-certificate`, `tls/sslmode-flag-overrides`, `tls/channel-binding-and-direct`, `tls/system-roots`, `tls/encrypted-client-key`, `tls/require-auth-and-version`, `cloud/token-auth-requires-tls`.

### Target database, overwrite and approval

A missing target is created (`Database "<name>" does not exist; creating`).
`--overwrite` drops and recreates it: on a terminal after the user types the
database name (a wrong name is exit 12); with `--force` after a countdown, with
a danger banner on a terminal unless `PGMI_NO_BANNER` is set, and without a
countdown when there is no terminal; without `--force` and without a terminal
it is refused with exit 12 and nothing is dropped. `--force` alone changes
nothing. End of input at the name prompt is exit 12 (`No input available`);
SIGINT at the prompt or during the countdown is 130 and drops nothing.
The recreated database keeps its owner, connection limit, database and
role-in-database settings, comment and privileges; a deploying role that
could not put one of them back is refused with exit 10 naming each item, and
nothing is dropped. Full reference: `docs/CLI.md` "--overwrite".

Cases: `exit-codes/12-overwrite-denied`, `flags/overwrite-force`, `flags/overwrite-restores`, `terminal/overwrite-confirmation`, `terminal/force-banner`, `terminal/non-interactive`, `terminal/prompt-cancel`.

### Concurrency

A deploy holds a session advisory lock keyed on the target database for its
whole run; a second deploy to the same target is exit 15. The lock is released
on every path, including a failure and a `deploy.sql` that ends inside an open
transaction.

Cases: `exit-codes/15-concurrent-deploy`, `execution/open-transaction`, `execution/tail-block-fails`.

### Execution contract

Everything up to the first top-level transaction terminator is sent as one
unit and runs as one transaction (the head); a `deploy.sql` without one is
entirely atomic. After it, each top-level statement runs on its own with
autocommit (the tail), so `CREATE INDEX CONCURRENTLY` works and an explicit
`BEGIN ... COMMIT` forms a real transaction. A deploy that ends inside an open
transaction fails with exit 13 and rolls it back. `--json` reports
`executionMode` (`atomic` or `psql`) and `executionUnits`/`unitsCommitted`. Full
reference: `docs/DEPLOY-GUIDE.md` "Atomic mode, then psql mode".

Cases: `execution/atomic-head`, `execution/tail-autocommit`, `execution/open-transaction`, `execution/tail-block-fails`.

### Test macro

`CALL pgmi_test()`, `CALL pgmi_test('pattern')`, `CALL pgmi_test('pattern',
'callback')`, also schema-qualified and in any letter case, is expanded before
`deploy.sql` reaches the server into savepoint-isolated test runs of the
`__test__/` files; never inside comments, string literals or dollar quotes.
Outside an explicit transaction block it fails with 25P01. `--json`
`testMacros` counts the expansions. Comments may stand between the macro's
tokens; only ASCII white space (space, tab, CR, LF, FF) separates them, so
after a no-break space or a vertical tab the text reaches the server
unexpanded. `NULL` as the pattern means every test. Full reference:
`docs/TESTING.md`.

Cases: `dialect/pgmi-test-macro`, `execution/pgmi-test-needs-begin`, `execution/pgmi-test-in-block`, `dialect/pgmi-test-macro-comments`, `dialect/pgmi-test-macro-whitespace`.

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

Cases: `notices/streaming`, `notices/severity`, `commands/verbose`, `terminal/colors`, `notices/summary-line`.

### The --json record

One JSON object on stdout at the end of every run, success or failure, with the
keys and meanings in `docs/CLI.md` "--json output" (`schemaVersion` 1). The
parsed value is the contract, not the bytes: Go escapes `<`, `>`, `&` as
`\u003c` and so on, and indents with two spaces.

Cases: `deploy/json-success`, `deploy/json-failure`, `errors/attribution`, `execution/atomic-head`, `config/dotenv`.

### Error attribution

A failure names where it happened: the line of a syntax error in `deploy.sql`,
the project file whose text was executing, and in `--json` the `sqlstate`,
`failedFile` and the other diagnostics listed in `docs/CLI.md`. A login without
the TEMPORARY privilege is told the `GRANT` it needs.

Cases: `errors/attribution`, `errors/head-syntax`, `errors/temporary-privilege`.

### Timeout, cancellation, interruption

`--timeout` (default 3m, Go duration syntax) cancels the running statement on
the server, gives the server 2 s to report the cancel, and is exit 16; running out while connecting is exit 11. SIGINT or
SIGTERM cancels and is exit 130; a second SIGINT ends at once, still 130.
Cancel works over TLS and over direct TLS negotiation.

Cases: `exit-codes/16-timeout`, `exit-codes/130-interrupted`, `exit-codes/130-sigterm`, `flags/timeout-forms`, `cancel/timeout-cancels`, `cancel/timeout-over-tls`, `cancel/sigint-twice`, `env/pgconnect-timeout`.

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
connector with IAM authentication. Lakebase: a token passed as the password,
no separate path. Real-cloud verification: Azure only (owner decision
2026-11-10, PGMI-845, pending); AWS and Google are checked against local fakes.

Cases: `cloud/azure-cli`, `cloud/azure-managed-identity`, `cloud/azure-powershell`, `cloud/azure-cloud-shell`, `cloud/azure-ml`, `cloud/azure-service-fabric`, `cloud/aws-iam-token`, `cloud/aws-credential-source`, `cloud/dotenv-feeds-sdk`, `cloud/token-auth-requires-tls`, `cloud/google-arguments`.
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
accepts them. The Rust binary accepts them with git's meaning.

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
No case: instance discovery always calls `login.microsoftonline.com`; verified
by the real-Azure runs (PGMI-845).

### sslrootcert=system refuses a weaker sslmode

Go (pgx) turns any sslmode into `verify-full` when `sslrootcert=system`, even
an explicit `disable` or `require`. Without an sslmode both binaries use
`verify-full`. With an explicit weaker sslmode the Rust binary refuses with
exit 10 (`weak sslmode "require" may not be used with sslrootcert=system`), as
libpq does.

Cases: `tls/system-roots` (the default). The refusal: Rust unit test
`system_roots_mean_verify_full`.

### Connection-string errors never echo the string

Go hides a URI that does not parse, but echoes a stray keyword
(`missing "=" after "junk"`), an ADO.NET part and a port value. The Rust
binary never quotes connection-string input in an error, on stderr, in
`--json` or in the MCP answer, and redacts the password and `sslpassword` from
every later refusal of the resolved connection.

Cases: `errors/connection-string-secrets` (the forms Go hides too); Rust unit
tests `errors_never_echo_the_input`, `errors_redact_the_password`.

### An unreadable .pgmiignore or a file name that is not UTF-8 is exit 10

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
behaviour in every case. The cases still pin Go's behaviour and flip when the
Rust binary lands.

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
`PGHOST`, as libpq does.

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
