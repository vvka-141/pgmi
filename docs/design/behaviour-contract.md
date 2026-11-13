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
12 overwrite not approved; 13 SQL failed; 14 `deploy.sql` missing (the message names the expected path and points
to `pgmi init`); 15 another
deploy holds the target; 16 `--timeout` exceeded after connecting; 130 SIGINT
or SIGTERM. Full table: `docs/CLI.md` "Exit Codes".
An error is one stderr line starting `pgmi: error: `. Usage errors include an
unknown subcommand, an unknown shorthand, and `-h` without a value on
`deploy` (it is `--host`; the message says so). More than one of `--azure`,
`--aws`, `--google` is exit 10. Booleans take `=false`, `--param` is never
split on commas, and `--timeout 0` turns the timeout off; help shows the
default as `3m0s`. Every boolean, `-v`, `--help` and `--version` too, takes
`=true`/`=false` (`1`, `t`, `T`, `TRUE`, `True` and their false forms) and
may be given twice; `--help` wins over a missing argument but not over a bad
flag value. A flag given twice takes its last value; `--param` and
`--params-file` keep every value. A flag that takes a value takes the next
argument even when it starts with `-` (`-d -x` names the database `-x`).
`-p` is an integer as Go reads one (`+5`, `0x10`, `010` is octal 8, `1_0`):
anything else is 2, `0` leaves the port to `PGPORT`, pgmi.yaml or 5432, and
one outside 1-65535 is 10 (`invalid --port flag: invalid port -1: must be
between 1 and 65535`).
A mistyped top-level command is followed by `Did you mean this?` and the
nearest command, as cobra prints it. An extra argument, also one after `--`,
is `accepts N arg(s), received M` (`accepts at most` where the argument is
optional), never an unknown command or flag; a missing project path is
`missing required argument: <project_path>`; a bad flag value is `invalid
argument "<v>" for "<flag>" flag: <why>` and a missing one `flag needs an
argument: <flag>`. A refusal names its next step: the flags that clash with
`--connection` and where their values go, a `Tip:` after a params file that
cannot be read or parsed, an example after a `--param` without `=`, the
line rule after the `.env` WARNING, the expected `<pgmi-meta>` layout after
malformed XML, and `--force` when the overwrite prompt gets no input.

Cases: `flags/repeated-flags`, `flags/bool-forms`, `flags/port-values`, `commands/usage-errors`, `commands/usage-error-lines`, `errors/next-step-hints`, `exit-codes/0-success`, `exit-codes/2-usage`, `exit-codes/10-invalid-config`, `exit-codes/11-connection-failed`, `exit-codes/12-overwrite-denied`, `exit-codes/13-sql-failed`, `exit-codes/14-no-deploy-sql`, `exit-codes/15-concurrent-deploy`, `exit-codes/16-timeout`, `exit-codes/130-interrupted`, `exit-codes/130-sigterm`, `commands/init-unwritable`, `commands/short-help-and-sslmode-value`.
Exit 3: no case; a release binary cannot panic on purpose. The Rust build proves it with its `testhooks` feature (`rust/pgmi/tests/panic.rs`): a panic exits 3 with the `--json` record, and a panicking serve tool answers `isError` with exit code 3.

## Commands

### info

`pgmi info [path]` summarises a project without a database: template, whether
`deploy.sql` and `pgmi.yaml` exist, file counts, directories; `--json` gives
the same as an object. A missing path is exit 10. It scans the project as
`deploy` and `metadata` do: a `<pgmi-meta>` block that does not parse, or a
`.pgmiignore` that hides `deploy.sql`, is exit 10 (`failed to scan
directory: ...`); `metadata plan`, `validate` and `scaffold` refuse that
`.pgmiignore` the same way.

Cases: `commands/info`, `commands/info-without-directories`, `commands/info-scaffolded-by`, `commands/info-metadata-refusals`.

### init

`pgmi init [path] [-t minimal|basic|advanced]` writes a template. Without `-t`
and without a terminal it writes `minimal`; on a terminal it runs the wizard. A
file named `<name>.scaffold` is written as `<name>`. A target holding anything
but `pgmi.yaml`, `.env` or `.git` is exit 2; an unknown template is exit 2.
A target that exists but cannot be listed, or is not a directory, is exit 1
and nothing is written. The Rust binary creates every file exclusively, so a
file that appears in the target meanwhile is never replaced: the write fails
with exit 1.
`{{PROJECT_NAME}}` in a template becomes the target folder's name, as is in
text files and JSON-escaped in `.json` files. Answering Yes to "Set up the
connection now?" runs the `config` wizard and writes its settings into the
scaffolded `pgmi.yaml`. The next step it prints names the path and database
as POSIX shell words, single-quoted unless plain (`pgmi deploy 'a b' -d mydb`).
Esc on the template screen is `init cancelled`, exit 12 (Go steps back to a
path screen first; the Rust binary has none, so its first Esc cancels);
Ctrl-C on any init screen is `init interrupted`, exit 130.

Cases: `terminal/init-wizard-esc-and-ctrl-c`, `commands/init-next-step-quoted`, `commands/init`, `commands/init-unwritable`, `commands/init-unlistable`, `terminal/init-wizard`, `terminal/non-interactive`, `commands/init-project-name`, `terminal/init-wizard-connection`, `terminal/init-wizard-connection-esc`.

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
elements in one comment are refused", and a repeated attribute such as two
`id`s), a block comment over 10240 bytes. A block is found as Go finds it,
by `<\s*pgmi-meta[\s>]` in a block comment, so `< pgmi-meta` (a space after
the `<`) or another element before the block is malformed metadata, exit
10, never a file without metadata (PGMI-942). A file without a block gets the id `md5("./<path>")` read as a
uuid, the same in `metadata plan` and `pgmi_plan_view.generic_id`.

Cases: `commands/metadata`, `commands/metadata-invalid`, `commands/metadata-duplicate`, `commands/metadata-scaffold-run-once`, `commands/metadata-forms`, `commands/metadata-refusals`, `commands/metadata-scaffold-and-checks`, `commands/metadata-tag-as-go-finds-it`.

### templates, version, ai, completion, help

`templates list|describe`, `version` and `--version` (`pgmi <version> (compat
1)`), `ai` (the overview) and `ai contract` (the session API), `completion
bash|zsh|fish|powershell`, `help <command>` and `--help`. An unknown
subcommand, template or shell, or extra arguments, is exit 2. `pgmi` with no
arguments prints the root help on stdout and exits 0; when interactive (stdin
and stdout on a terminal, no `CI`, no `PGMI_NON_INTERACTIVE=1`) it draws the
banner on stderr instead, unless `PGMI_NO_BANNER` is set. `deploy --help`
describes the command, its examples, where the password and parameters come
from, the exit codes in two columns, and every flag. The completion scripts
are Cobra's: each asks the binary for the words (`pgmi __complete <words>`,
one per line with a tab and its description, `__completeNoDesc` without, then
`:<directive>`). They offer subcommands, flags (`-` typed), `--sslmode`
values, `init --template` and `templates describe` names, the `completion`
shells, and directories for a project path.

Cases: `commands/bare-not-interactive`, `commands/deploy-help-exit-codes`, `commands/completion-words`, `commands/templates`, `commands/version`, `commands/ai`, `commands/completion`, `commands/help`, `commands/bare`, `commands/deploy-help-text`.

### Verbose output

`-v` shows `RAISE DEBUG` from `deploy.sql`; without it, DEBUG is hidden and
NOTICE, INFO and WARNING are shown. With `-v` every notice line (DETAIL and
HINT included) starts with the seconds since the deploy started, `[1.23s] `;
no other line does (`serve` prints notices without it). No server DEBUG line
from pgmi's own queries reaches stderr: the message level is raised only after
`CALL pgmi_test()` is expanded.

`-v` names each step on stderr as `[VERBOSE] ...` lines, in Go's order and
wording: `Using --database flag (<db>) instead of connection string database
(<db>)` when `-d` replaced the string's database; the `Connection resolved:`
block (host, port, user, target and maintenance database, SSL mode, SSL
cert/key/root cert paths when set, auth method; never a password or token);
`Deploying to database`, `Source path`, `Scanning`, `Found N files to load`;
`Database "<db>" already exists`, or `not found; using the maintenance database
to create it`, `Connecting to maintenance database`, `Created database`; under
`--overwrite` `Connecting to maintenance database`, `exists; requesting
approval`, `Preserving encoding E, collation C across the recreate`, `DROP
DATABASE`, `CREATE DATABASE`; then `Connecting to database`, `Creating pg_temp
internal tables`, `Loading files into pg_temp._pgmi_source`, `Loading
parameters into pg_temp._pgmi_parameter`, `Applying API contract`, `Applied API
contract v1`, `TLS: <version|none> (sslmode=<mode>)` before `Executing
deploy.sql`, and after it `Expanded N test macro(s) in deploy.sql` and
`deploy.sql splits into N execution units ...` when there are any. Ignored
paths follow the summary line as `  ignored <path> (<rule>)`, not with
`--json`. `init -v` names the project, every directory and file it creates
(`Skipping existing: pgmi.yaml` for a kept one) and `Project created
successfully`; `metadata plan|validate -v` names the project path and the JSON
switch, `metadata scaffold -v` the path, preview and default idempotent.
Parameter loading prints counts only, never a key or a value:
`[VERBOSE] Loaded N parameter(s) from PGMI_PARAM_* environment variables`,
per params file `[VERBOSE] Loading parameters from file: <path>` and
`[VERBOSE] Loaded N parameter(s) from file (total: M)`, and
`[VERBOSE] CLI parameters override N value(s)`.

Cases: `commands/verbose`, `commands/verbose-lines`, `notices/verbose-elapsed`, `notices/verbose-no-internal-debug`, `notices/verbose-no-server-debug`, `params/verbose-counts`.

### serve

`pgmi serve` is an MCP server over stdio (JSON-RPC 2.0, one message per line):
`initialize`, `tools/list` with seven tools (`deploy`, `init`, `metadata_plan`,
`metadata_validate`, `templates_list`, `ai_overview`, `ai_contract`),
`tools/call`, `ping`. A malformed line gets -32700 with a null id, an unknown
method -32601, and the session goes on. Calls run concurrently: a ping is
answered during a deploy, and `notifications/cancelled` stops a running deploy,
which rolls back. End of input exits 0. Full reference: `docs/CLI.md`
"pgmi serve".

Once input ends or SIGINT/SIGTERM arrives, serve keeps writing answers as
long as the client takes them; a client that takes nothing for 2 s ends
the session, running calls are cancelled and serve exits 0. A client that
reads slowly but steadily gets every answer, each a complete line, however
long that takes: 8 KB a second through eight 16 KB answers, 800 KB a second
through one 3.5 MB answer. In the Rust binary on Linux, where stdout is a
pipe, any byte taken within each 2 s counts, so 1 KB and 256 bytes a second
through a one-page pipe get every answer too; on a socket, macOS or Windows
a client must take a 512-byte write within each 2 s once its buffer is
full (see "serve bounds what a client can make it hold"). In the Rust binary a second SIGINT or SIGTERM ends
serve at once (see "serve quits at a second signal"). A failed write to stdout ends the session: running calls are
cancelled and nothing more is read (Go stops reading only when an answer
written by the reader fails, and then waits for the running calls). A failed
read of stdin or write of stdout exits 1 with `pgmi: error: read request: ...`
or `pgmi: error: write response: ...` (Go: the read or write error itself);
end of input, a signal and a client dropped for not reading exit 0. On Unix
a stdout pipe the client closed is not a failed write: both binaries keep
SIGPIPE's default action, so serve dies of the signal (a shell reports 141). Rust unit tests
`a_client_that_stops_reading_does_not_hold_up_the_stop`,
`an_output_failure_ends_the_session`, `a_failed_read_ends_the_session_with_exit_1`,
`the_end_of_input_ends_the_session_successfully`,
`a_slow_steady_reader_gets_every_answer_after_the_input_ends`,
`a_client_that_never_reads_ends_two_seconds_after_its_input`,
`a_steady_reader_behind_a_buffering_stdout_gets_a_huge_answer_whole`; Rust
integration tests over the real binary's pipes (`rust/pgmi/tests/serve_pipes.rs`):
`a_reader_taking_8_kb_a_second_gets_every_answer_after_the_input_ends`,
`a_huge_answer_reaches_a_reader_taking_800_kb_a_second_whole`,
`a_burst_of_3000_requests_with_a_reader_gets_every_answer`,
`a_client_that_never_reads_ends_about_two_seconds_after_its_input`.

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

A client that reads its answers while it pipelines thousands of requests gets
every answer in order.

Cases: `serve/session`, `serve/deploy-verbose`, `serve/deploy-timeout-zero`, `serve/burst`, `serve/slow-reader-many-answers`, `serve/slow-reader-huge-answer`, `serve/deploy-tool`, `serve/cancel`, `serve/malformed`, `serve/protocol-versions`, `serve/deploy-guards`, `serve/overwrite-needs-operator-switch`, `serve/deploys-in-sequence`, `serve/concurrent-deploys`, `serve/deploy-timeout`, `serve/deploy-warning-lines`, `serve/deploy-param-key`, `serve/deploy-password-masked`.

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
file before anything else sees it. Each project file is capped at 10 MiB, deploy.sql never
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
`info`. In `deploy` these refusals, `.pgmiignore`'s and a `<pgmi-meta>` block
that does not parse start with `failed to scan directory "<path>": `; in
`info`, `metadata plan` and `metadata scaffold` with `failed to scan
directory: `, in `metadata validate` with `validation failed: `, as in Go.
The first SIGINT or SIGTERM during the load is exit 130 and a
`--timeout` that runs out there is exit 16, as in Go, which loads after
connecting.

Cases: `dialect/pgmiignore`, `dialect/pgmiignore-characters`, `dialect/root-deploy-file-any-case`, `dialect/pgmiignore-hides-deploy`, `execution/loader-at-scale`, `dialect/project-walk`, `commands/metadata-refusals`, `errors/special-file`, `dialect/nested-deploy-sql-loaded`, `dialect/pgmiignore-broken-patterns`, `formats/binary-file-in-test-directory`. The load racing a signal and the timeout: Rust unit tests `blocking_work_gives_way_to_the_first_signal`, `blocking_work_gives_way_to_the_timeout`.

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
pgmi's own plan queries see statistics (case `deploy/session-tables-analyzed`). `pgmi_test_directory_view` lists every directory that holds a test-path file, SQL or not (case `execution/test-directory-without-sql`). A `<pgmi-meta>` block without `<description>` gives `''` in `pgmi_source_metadata_view.description`, never NULL (case `deploy/metadata-description-default`). Full reference: `docs/session-api.md`.

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
`--param` without `=`, a missing params file, a params file (or `-`) over
`PGMI_MAX_FILE_SIZE` (Rust only), two keys differing only in case
within one source (one file, the `--param` flags, the `PGMI_PARAM_*`
variables, `pgmi.yaml` `params`; across sources they are one parameter and the
higher source wins), a `PGMI_PARAM_` with nothing after the prefix, a
`PGMI_PARAM_*` variable that is not valid UTF-8, a key that is not a
PostgreSQL identifier of 1-63 characters (letter or underscore first, then
letters, digits, underscores), and a value holding a NUL byte. All are refused
before connecting. Full reference: `docs/CLI.md`
"Parameter Flags".

Cases: `params/env-var-reaches-session`, `params/precedence`, `params/invalid-key`, `params/case-twins`, `flags/params-file`, `flags/params-file-pipe`, `config/precedence`, `config/dotenv`, `exit-codes/10-invalid-config`.

### Connection sources

The connection comes from `--connection`, else `PGMI_CONNECTION_STRING`, else
`DATABASE_URL`, else `-h/-p/-U` with the `PG*` variables, else `pgmi.yaml`,
else defaults (localhost:5432, maintenance database `postgres`). A connection
string may be a URI, libpq keyword/value (spaces around `=`, quoted values
holding `;`) or ADO.NET (a value quoted with `'` or `"`, a doubled quote
inside; a port in `Host=h:port` wins over `Port=` wherever it stands;
`dbname` and any libpq keyword spelled exactly are taken as written). In a URI
query only `%XX` is decoded (`+` stays a plus), keys are read in any case,
`applicationname` and `connecttimeout` stand for `application_name` and
`connect_timeout`, and a repeated key takes its first value. As in Go: an
empty connection flag (`-h ''`, `-U ''`, `-d ''`, `--sslcert ''`, `--sslkey ''`,
`--sslrootcert ''`) is unset, so the next source applies and is announced; an
empty `sslmode` in any form of string is unset; ADO.NET text after a closing
quote is dropped up to the next `;`; a keyword/value `connect_timeout` that is
not a number is refused even when the key comes again with a number (any other
repeated keyword takes its last value) (case `connection/empty-and-trailing-values`). `-d` (or `PGDATABASE`, or
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

Cases: `flags/connection-forms`, `connection/string-forms`, `flags/granular-flags`, `env/connection-string-vars`, `env/connection-string-vars-refuse-flags`, `env/pg-variables`, `env/pgservice-refused`, `config/pgmi-yaml`, `config/pgmi-yaml-invalid`, `config/precedence`, `config/overrides`, `config/dotenv-provenance`, `errors/connection-source`, `env/passfile-beats-pgpassfile`, `connection/uri-plus`, `connection/uri-key-case`, `connection/uri-repeated-key`, `connection/ado-libpq-keywords`.

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
on stderr; other failures are not retried. In the Rust binary, when those 4
tries give up on a transient failure, the whole deploy runs again inside
`--timeout` (see "Whole-deployment retry"), so a server that stays
unreachable ends the run when the time box would end inside the next wait. `max_protocol_version` and
`min_protocol_version` (`3.0`, `3.2`) are accepted, and a deploy and a cancel
work with both set to `3.2` (Go speaks 3.2 then; the Rust binary refuses a
minimum above 3.0, see "A minimum protocol above 3.0 is refused for now");
`sslsni`, `sslcertmode` and
`client_encoding` are accepted. `connect_timeout` (and `PGCONNECT_TIMEOUT`) is
whole seconds; in the Rust binary it bounds each attempt from
before the host lookup and the sign-in token, so a stalled host is given up
and the next one tried; a round whose hosts all ran out of it is not
retried, as in Go (PGMI-933, which reverses PGMI-907), and a value above 9223372036 (the longest Go
duration, which Go wraps around) is refused before connecting, exit 10.
A refusal ends with its next step: a missing maintenance database says what
it is and to name an existing one (the connection string's database or
`connection.maintenance_database`); 53300 that the server is at
`max_connections`; 57P03 that it is starting up; a connection the server
closed early to check `pg_isready` (never a TLS hint). A server error keeps
its own severity (`FATAL:`, `ERROR:`). A wrong password says to check
`$PGPASSWORD`, `~/.pgpass` or the connection string, a missing CONNECT
privilege gives the `GRANT CONNECT`, and a host DNS cannot find says to check
its spelling, DNS and `$PGHOST`. The server's DETAIL follows the error as a
`DETAIL:` line and is `detail` in `--json`.

Cases: `errors/connection-failures`, `errors/connect-refusal-detail`, `connection/connect-timeout-range`, `env/pg-tls-protocol-variables`, `connection/pgpass`, `connection/pgpass-wildcard-and-file`, `connection/pgpass-world-readable`, `connection/multi-host`, `connection/target-session-attrs`, `connection/keywords`, `connection/unix-socket`, `env/pg-variables`, `env/pgappname`, `env/pgconnect-timeout`, `env/ignored-variables`, `errors/connection-failures`, `exit-codes/11-connection-failed`, `connection/protocol-version`, `bad-weather/connect-resets-retried`, `bad-weather/startup-refusals-retried`, `bad-weather/startup-refusals-final`, `retry/connect-gave-up`.

### TLS

sslmode `disable`, `allow` (plaintext), `prefer` (TLS when the server offers
it), `require` (no verification, unless a root certificate is given, which makes
it `verify-ca`), `verify-ca` (chain only), `verify-full` (chain and host name;
an IP address needs an IP SAN). A server certificate marked `CA:TRUE`, as
`openssl req -new -x509` makes it, verifies when it is itself in the root
file or a root signed it directly. A refused certificate is exit 11 and worded
as Go's x509 words it (`x509: certificate signed by unknown authority`, `...
has expired or is not yet valid`, ...). The root certificate is `sslrootcert`, else
`~/.postgresql/root.crt`, else the platform trust store (as pgx does; libpq
would refuse); `sslrootcert=system` names the platform trust store
explicitly. `SSL_CERT_FILE` replaces the platform store. The client certificate is `sslcert`/`sslkey`, else
`~/.postgresql/postgresql.crt`/`.key`; an encrypted key opens with
`sslpassword`, a wrong one is exit 10. Each of the three paths comes from
the flag, else the connection string, else `PGSSL*`, else `pgmi.yaml`, with or
without a connection string. `channel_binding=require` needs TLS;
`sslnegotiation=direct` is supported; `require_auth` refuses another method,
and a method it does not know is exit 10 before any host lookup.
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
database name after `Type the database name to confirm: `, on that line (a
wrong name is exit 12); with `--force` after a five-second countdown that
rewrites one line with `\r` and ends `now.`, with
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
`recreated: true`. A create that fails after the drop (`55006`, a session
on the template database) or runs past that bound leaves the database
dropped; the Rust binary's error then begins `database "<db>" was dropped
and not recreated; run the deploy again` (PGMI-929; Go gives the bare
error).
The recreated database keeps its owner, connection limit, database and
role-in-database settings, comment and privileges; a deploying role that
could not put one of them back is refused with exit 10 naming each item, and
nothing is dropped. A database PostgreSQL marked invalid (connection limit
-2, left by an interrupted `DROP DATABASE`) comes back with no connection
limit. `--overwrite` of `template0` or `template1`, in any letter case, is
exit 10 before connecting, as in Go; the Rust binary also refuses any other
database the server marks `datistemplate` once connected (case
`flags/overwrite-template-database`). Full reference: `docs/CLI.md` "--overwrite".

Cases: `exit-codes/12-overwrite-denied`, `flags/overwrite-force`, `flags/overwrite-connection-limit`, `flags/overwrite-invalid-database`, `flags/overwrite-new-database`, `flags/overwrite-restores`, `terminal/overwrite-confirmation`, `terminal/force-banner`, `terminal/force-countdown-timeout`, `terminal/non-interactive`, `terminal/prompt-cancel`, `connection/maintenance-database-only-when-needed`, `bad-weather/overwrite-signal-between-drop-and-create` (the fault-injecting proxy holds the `CREATE DATABASE`), `a_signal_after_the_drop_still_recreates_and_restores_the_database`, `a_failed_create_after_the_drop_says_the_database_is_gone`, `terminal/overwrite-prompt-and-countdown`.

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

Both locks are released on the server before pgmi disconnects, after a
failure too, so a rerun is never refused because the closed connection reached
the server late. A connection lost while holding the deploy lock takes the lock
with it once the server notices.

Cases: `exit-codes/15-concurrent-deploy`, `execution/open-transaction`, `execution/tail-block-fails`, `flags/overwrite-concurrent`, `flags/overwrite-deploy-during-approval`, `bad-weather/overwrite-slow-close`, `bad-weather/setup-failure-slow-close`, `bad-weather/reset-in-session-setup`, `bad-weather/reset-at-lock-release`. Rust integration test `a_held_overwrite_lock_refuses_a_second_overwrite_with_exit_15`.

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

A connection lost while `deploy.sql` runs (reset, or the server ending the
session with FATAL) is exit 13 with the server's text; what the head or
earlier tail statements committed stays. Lost while the session tables are
created, before `deploy.sql`, it is exit 1. A lock held by another session is
waited for; `lock_timeout`, a deadlock (40P01) and a serialization failure
(40001) are exit 13 like any SQL error, and pgmi never retries them.

Cases: `execution/atomic-head`, `execution/tail-autocommit`, `execution/tail-semicolon-in-parentheses`, `execution/open-transaction`, `execution/tail-block-fails`, `execution/commit-and-chain`, `bad-weather/reset-in-transaction`, `bad-weather/reset-in-tail`, `bad-weather/server-errors-mid-deploy`, `bad-weather/lock-wait`, `bad-weather/lock-timeout`, `bad-weather/deadlock`, `bad-weather/serialization-failure`.

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
(two spaces after the colon). LOG and DEBUG reach stderr and `--json` only when
`deploy.sql` lowers `client_min_messages` itself (or DEBUG under `-v`), as bare
messages; the server's own DEBUG never does. The run ends with one summary line: `ok
<db>: N files loaded in T` or `FAILED <db>: failed after T ...` when stderr is
not a terminal, `✓`/`✗` in colour on a terminal; with `NO_COLOR` or
`TERM=dumb` the words again.
T is seconds with two decimals (`1.12s`). A success adds `, N paths ignored`
when discovery left paths out (dot-files count). A failure adds what was
left: ` — nothing committed` in atomic mode, ` (unit U of N failed; C earlier
unit(s) already committed)` in psql mode, and `created database "<db>", left
empty` when this run created it. A run refused before the deploy starts (the
connection cannot be resolved, a parameter source is refused) prints no
summary line, only the error.

Cases: `errors/refused-before-deploy-no-summary`, `errors/refusal-wording`, `notices/streaming`, `notices/severity`, `notices/deploy-sql-message-level`, `commands/verbose`, `terminal/colors`, `notices/summary-line`, `notices/secrets-redacted`, `execution/pgmi-test-without-tests`, `execution/setup-psql-fixture`, `terminal/no-color-words`.

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
zero and false until known. `database` and `target` follow the same line: null
until the deploy starts (a parameter source that cannot be read, a `--param`
without `=`), set from then on (a refused parameter key or value, a missing
`deploy.sql` included), never one without the other; `target` lists `host`,
`port`, `database` in that order. A DETAIL, HINT or WHERE field the server
sent empty is left out, on stderr, in `--json` and in a notice, as in Go.

Cases: `deploy/json-success`, `flags/json-database-before-load`, `flags/json-record-once-deploy-starts`, `flags/compat-record`, `deploy/json-failure`, `errors/attribution`, `errors/diagnostics-secrets`, `execution/atomic-head`, `config/dotenv`. The Rust redaction: unit test `a_failure_record_redacts_detail_hint_and_where`.

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
without the TEMPORARY privilege is told the `GRANT` it needs, naming the
database and the role. A failure while pgmi fills the session tables starts
`session preparation failed: ` and is not placed in `deploy.sql`.

Cases: `errors/attribution`, `errors/head-syntax`, `errors/temporary-privilege`, `errors/session-preparation`, `errors/line-and-column`, `errors/parameter-value-not-in-statement-echo`.

### Timeout, cancellation, interruption

`--timeout` (default 3m, Go duration syntax) cancels the running statement on
the server, gives the server 2 s to report the cancel, and is exit 16; running out while connecting is exit 11 with the same message. SIGINT or
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
`a_rollback_the_server_never_answers_does_not_hang_the_deploy`). A server
that freezes, answering neither the session nor the cancel request, still ends
the run within 2.5 s of `--timeout`. A signal while connecting, between
connection attempts, while the session tables are created, in the tail or at
the lock release is exit 130 at once; in the tail the running statement is
cancelled and the earlier ones stay committed. In the Rust binary
`--timeout` is the time box of every attempt of a whole-deployment retry and
the waits between them (see "Whole-deployment retry"): a deadline inside an
attempt after an earlier one failed says `time box of <d> used up after N
attempts; last error: ...`, a wait is never started past the deadline, and a
signal during a wait is exit 130 at once.

Cases: `retry/deadline-during-an-attempt`, `retry/wait-cut-at-the-deadline`, `retry/signal-during-a-wait`, `config/pgmi-yaml-timeout-too-long`, `exit-codes/16-timeout`, `errors/timeout-while-connecting`, `exit-codes/130-interrupted`, `exit-codes/130-sigterm`, `flags/timeout-forms`, `cancel/timeout-cancels`, `cancel/timeout-over-tls`, `cancel/sigint-twice`, `env/pgconnect-timeout`, `cancel/ctrl-c-json-exit-code`, `cancel/timeout-leaves-invalid-concurrent-index`, `cancel/stopped-record`, `bad-weather/frozen-server-timeout`, `bad-weather/signal-while-connecting`, `bad-weather/signal-during-connect-backoff`, `bad-weather/signal-during-session-setup`, `bad-weather/signal-in-tail`, `bad-weather/signal-at-lock-release`, `bad-weather/lock-wait-timeout`.

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
empty). `.env` reads every form as godotenv 1.5 does: `KEY: value`, `export`
followed by any blank, `\$` for a literal dollar, `\n` and `\r` the only
escapes in double quotes (`\t` is `t`), `$name` in lowercase left as written,
the last ` #` starting a comment, and an assignment after a closing quote on
the same line; in the Rust binary `\"` and `\\` just before the closing double
quote read as `"` and `\`, a leading byte-order mark is skipped (godotenv
misreads the first and refuses the second), and a line with no key, such as
`=x` or a last line without `=` or a newline, makes the file unparseable
(godotenv applies the rest and tries to set an empty name); a `.env` that does not parse applies nothing and prints a WARNING
naming the file, never its text. A NUL byte anywhere in the `.env` makes it
unparseable in the Rust binary (Go drops a pair whose value holds one and
applies the rest); neither binary prints the value. `.env` values enter the process environment,
so they reach the cloud sign-in too; a params
file additionally refuses, with exit 10, a backslash or `$` inside double quotes
and an unquoted ` #`, which `.env` readers disagree on. `pgmi.yaml` values
become text: `~` is empty, numbers and booleans as written (`5432.0` stays
`5432.0`), block scalars keep their final newline.

In the Rust binary a params file, `-` and the project `.env` are read up to
`PGMI_MAX_FILE_SIZE` (10 MiB by default) and a larger one is exit 10; a
password file over 10 MiB is not read, with a WARNING (Rust unit test
`a_password_file_over_the_limit_is_not_read_and_warned_about`).

Cases: `formats/dotenv-edges`, `formats/dotenv-expansion`, `config/dotenv-forms`, `formats/dotenv-nul`, `cloud/dotenv-feeds-sdk`, `formats/params-file-edges`, `formats/pgmi-yaml-edges`, `config/pgmi-yaml-invalid`, `formats/size-limits`, `config/dotenv-size-limit`, `config/yaml-anchors`, `config/yaml-block-scalars`, `config/yaml-comments`, `config/yaml-booleans`, `config/yaml-numbers`, `config/yaml-tabs`, `config/yaml-bom`, `config/yaml-crlf`, `config/yaml-unknown-keys`, `config/yaml-duplicate-keys`, `config/yaml-port`, `config/yaml-timeout`, `config/yaml-empty`, `config/yaml-documents`, `config/dotenv-bom`, `config/dotenv-refused-keys`.

### Terminal behaviour

Wizards and prompts run only on a terminal and never when `CI` is set or
`PGMI_NON_INTERACTIVE=1`. In the Rust binary `deploy` never opens a wizard:
with no connection it exits 10 and lists the ways to set one (owner
2026-11-12); `pgmi init` and `pgmi config` keep theirs. Long output (`pgmi ai`) goes through `PGMI_PAGER`,
else `PAGER`, else `less -R`, on a terminal only; `PGMI_PAGER=cat` prints
directly. The pager command is split on white space and run without a shell
(no quoting, no `$` expansion); a pager that cannot start means the output is
printed directly. Wizards and the pager need stdin and stdout on a terminal
and neither `CI` nor `PGMI_NON_INTERACTIVE=1`, prompts stdin and stderr. Outside the wizards pgmi never sends the terminal a
query (background colour, cursor position, device attributes), so a terminal
that never answers cannot stall it.

Cases: `terminal/init-wizard`, `terminal/non-interactive`, `terminal/pager`, `terminal/pager-not-interactive`, `terminal/colors`, `terminal/pager-command`, `terminal/no-terminal-queries`.

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
A sign-in endpoint that fails briefly is asked again, as the Go SDKs do: a
managed identity endpoint other than IMDS (App Service, Arc, Cloud Shell,
Service Fabric, Azure ML) on 408, 429, 500, 502, 503, 504 or no answer (4 attempts,
waits 0.8 s, 2.4 s, 5.6 s); the Cloud SQL Admin API on 5xx (5
attempts, waits from about 0.3 s growing by 1.6); a Google access token on
an error its library calls transient; the AWS container credentials
endpoint on 5xx. When every attempt fails the error
says how many were made. The Rust binary also retries an Admin API call that
got no answer, which Go's connector does not.

Cases: `cloud/azure-msi-retry`, `cloud/azure-cli`, `cloud/azure-managed-identity`, `cloud/azure-powershell`, `cloud/azure-cloud-shell`, `cloud/azure-ml`, `cloud/azure-service-fabric`, `cloud/aws-iam-token`, `cloud/aws-credential-source`, `cloud/dotenv-feeds-sdk`, `cloud/token-auth-requires-tls`, `cloud/google-arguments`, `cloud/azure-client-id-never-uses-the-cli`, `cloud/aws-session-token-and-profile`, `cloud/cloud-ids-env-beats-yaml`, `bad-weather/azure-managed-identity-retries`, `bad-weather/azure-managed-identity-gives-up`, `bad-weather/aws-credential-endpoints-retried`.
Service principal and the real Cloud SQL connector: no case; neither can be
pointed at a local fake from the Go binary (see `blackbox/COVERAGE.md` "Cloud
sign-in"); the Rust binary is checked against the runner's fakes by Rust
integration tests (`rust/pgmi/tests/google_cloud_sql.rs`, and
`azure_client_secret.rs` for a service principal with `AZURE_CLIENT_SECRET`). The
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
from `::1` and is retried as one. The Rust binary reports each address
tried, and a certificate failure among them ends the connect at once.

Cases: `tls/system-roots`, `tls/verify-without-root`.

### POSIX character classes in .pgmiignore

Go refuses `[[:space:]]` and similar as an invalid pattern (exit 10); git
accepts them. The Rust binary accepts them with git's meaning; the case checks
that `sp[[:space:]]ace.sql` hides `sp ace.sql` and keeps `spxace.sql`.

Cases: `dialect/pgmiignore-posix-class`.

### Backslash inside a class in .pgmiignore

Go reads a backslash inside `[...]` as a literal backslash, so `a[\]]b.sql`
is the class `[\]` followed by `]b.sql` and hides a file named `a\]b.sql`.
git's wildmatch, and the Rust binary, read it as an escape: the class holds
`]`, and the pattern hides `a]b.sql`. The case checks that Rust hides
`a]b.sql` and keeps `axb.sql`, while Go keeps both.

Cases: `dialect/pgmiignore-class-escape`.

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

An unknown option is named only when no password is written before it: an
unquoted password holding a `;`, a space or a `&` splits there, and the
option after it is the password's rest, so the refusal says the option is not
shown and how to quote the password. A percent sign in a URI takes exactly
two hex digits (`%+F` is refused, as Go's URL parser does), and a URI
`#fragment` is dropped, as in Go. A keyword/value string is ADO.NET only with
an unquoted semicolon, or a single capitalised pair (`Password='p;q'`), so
`HOST=h password=x` is keyword/value and its unknown key is refused (exit 10);
an unknown "key" that is not key-shaped (it holds `:`, `/`, `@` or `?`) is
never named.

Cases: `connection/keyword-uppercase-no-echo`, `connection/uri-scheme-case`, `errors/connection-string-secrets` (the forms Go hides too),
`errors/connection-string-password-rest`; Rust unit tests
`errors_never_echo_the_input`, `errors_redact_the_password`,
`an_unknown_key_beside_a_password_is_not_named`,
`percent_escapes_take_two_hex_digits`, `a_uri_fragment_is_dropped`, and the
property check `conn_forms_agree_and_hide_the_password`.

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
which it cannot check. Owner-decided 2026-11-11 for the refusals.

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
  A call keeps its place until its answer is queued, so finished calls
  whose answers wait count too. `ping`, `tools/list` and
  `notifications/cancelled` are still served. A cancelled call keeps its
  place until every blocking piece of it really ends: a tool that reads
  files, and a `deploy`'s reads of `pgmi.yaml`, params files, the project
  and `.pgpass`. So cancelling calls never piles up more than 32 of them.
  A `deploy` reading a `paramsFile` that is a pipe stops waiting within
  0.1 s of its cancel (Unix); one opening a named pipe that no writer has
  opened keeps its place until a writer comes, as the open blocks. `init`,
  which writes, finishes even when cancelled.
- When 1024 answers are waiting, serve reads no further request, and a
  finished call does not queue its answer, until the client takes one; a
  client that takes nothing for 2 s ends the session: running calls are
  cancelled and serve exits 0 as at the end of input. A client that keeps
  sending tool calls and never reads is therefore dropped about 2 s after
  its answers fill the queue, holding at most 1024 answers and 32 calls. A
  client that reads is never dropped, however many requests it pipelines
  and however long one answer takes to read. "Takes" is measured in
  bytes on Linux when stdout is a pipe (`FIONREAD` changes); elsewhere no
  call reports what a reader took, and progress is a 512-byte write
  completing, so there a client reading less than 256 bytes a second can be
  dropped once its output buffer is full.

Cases: none for the limits (Go accepts all three); `serve/burst` pins that a
reading client is served in full. Rust unit tests
`a_client_that_reads_while_it_sends_a_burst_gets_every_answer`,
`a_line_over_16_mib_is_refused_at_once_and_the_session_goes_on`,
`calls_beyond_32_are_refused_and_cancels_still_get_through`,
`a_cancelled_blocking_call_stops_waiting_but_keeps_its_slot`,
`a_cancelled_init_still_writes_the_whole_project`,
`a_client_that_never_reads_is_dropped_once_its_answers_pile_up`,
`a_client_that_keeps_calling_and_never_reads_is_dropped_with_its_calls_bounded`,
`a_reader_keeps_its_session_while_a_huge_answer_holds_up_a_full_queue`,
`a_line_over_the_limit_is_dropped_and_counted_once`; Rust integration tests
`a_client_that_keeps_calling_and_never_reads_is_dropped_with_bounded_memory`
(RSS stays under 150 MB; it peaked near 32 MB in a release build),
`a_reader_taking_800_kb_a_second_is_kept_while_a_huge_answer_holds_up_a_full_queue`,
`a_reader_taking_1_kb_a_second_through_a_one_page_pipe_gets_every_answer_whole`,
`a_reader_taking_256_bytes_a_second_through_a_one_page_pipe_gets_every_answer`,
`cancelled_deploys_reading_a_pipe_that_never_ends_give_their_threads_back`,
`deploys_cancelled_while_a_pipe_has_no_writer_keep_their_places_until_it_opens`
(`rust/pgmi/tests/serve_pipes.rs`), and unit tests
`a_reader_slower_than_a_line_per_drain_keeps_its_session_while_the_backlog_moves`,
`blocking_work_keeps_the_slot_after_its_caller_stops_waiting`,
`a_wait_on_a_named_pipe_ends_when_it_is_cancelled`.

### serve quits at a second signal

Owner-decided 2026-11-12 (fix round 5).

At the first SIGINT or SIGTERM both binaries stop reading, cancel running
calls and keep writing the answers already made to a client that takes them.
Go keeps writing until every answer is taken, however slowly, and ignores
further signals (with input still open it does not exit at all). The Rust
binary ends at once with exit 130 at a second SIGINT or SIGTERM, as `deploy`
does; answers not yet written are lost.

Cases: `serve/second-signal`; Rust integration test
`a_second_signal_ends_serve_at_once_while_it_drains_to_a_slow_reader`.

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

### A URI query key that is no keyword is a server setting

Owner-decided 2026-11-12 (PGMI-898), replacing the decision of 2026-11-10,
which rested on a wrong belief about Go. As Go, a URI query key that is not
a connection keyword goes to the server as a setting for the session
(`?search_path=app&statement_timeout=5s` deploys with both set); the server
refuses a name it does not know (exit 11, `unrecognized configuration
parameter`). In the keyword/value form an unknown key is still refused before
connecting (exit 10), as Go does. The three keys `docs/CONNECTIONS.md`
documents for single-session servers (`default_query_exec_mode`,
`statement_cache_capacity`, `description_cache_capacity`) keep their
documented meaning.

Cases: `connection/keywords`, `connection/uri-server-settings`.

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
binary writes a fresh connection block; Go edits the file in place. `pgmi
init`'s connection wizard asks no such question, so the Rust binary keeps a
`pgmi.yaml` it cannot parse or read (invalid UTF-8, UTF-16, no permission),
warns `could not save connection` and still exits 0; Go replaces the file. An
`AZURE_TOKEN_CREDENTIALS` value that names no credential is refused before
connecting with exit 10 (Go: 1). Both wizards draw full-screen, as Go's do.

Cases: `cloud/azure-token-credentials-invalid` (the exit code),
`cloud/azure-tenant-id-refused` (a tenant id outside letters, digits, `.` and `-`),
`terminal/init-wizard-keeps-unparseable-yaml` (init keeps the file). The fresh
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

### Whole-deployment retry (owner, 2026-11-12)

New in the Rust binary (record 0025, PGMI-886); Go ends the run at the
first failure. When an attempt fails for a transient reason, pgmi closes its
session and runs the deploy again from the start on a new connection: a new
session, the files loaded again, deploy.sql from its first line, exactly
like a redeploy. Transient: SQLSTATE class 08 and a connection closed, reset
or refused before deploy.sql runs (a connection lost while a unit of
deploy.sql runs leaves that unit's outcome unknown and is never retried, even
with `retry_after_commit`: exit 13, `not retried: unit U of T got no answer
from the server, so whether it committed is unknown`, owner 2026-11-13,
PGMI-970); 57P01, 57P02, 57P03; 53300; 40001, 40P01; 55P03; 25006
only while the server reports `in_hot_standby = on` and the connection did
not ask for a standby (`target_session_attrs` `read-only`, `standby`,
`prefer-standby`); a cloud sign-in that still fails after its own retries
(no answer, or a busy answer every time); the connect retry giving up on a
transient failure. Never retried: any other SQL error, any failure inside a
test (`phase` `test`), configuration errors (exit 10), a refused sign-in
(28xxx, 401/403), a concurrent deploy (exit 15), anything after a signal or
a cancel. Only a run whose attempt has reached the server (the session
opened) is retried: a first attempt that never connects, whether refused,
unreachable, busy or with a sign-in that gave up, fails at once as in Go
(owner decision PGMI-909). `--overwrite` drops and recreates once, before
the first attempt; its own failure is final, and a retry never drops again
or asks again.

A retry whose deploy lock is held by an earlier attempt's own server
session (pgmi recorded that backend's pid when it took the lock; the
connection was cut and the server has not noticed) ends that session with
`pg_terminate_backend`, printing `pgmi: the deploy lock is held by this
deploy's earlier session (pid N), which the server has not noticed is
gone; ending it`, and waits up to 10 s for the lock (PGMI-919). If the role
may not end it, or the lock stays held, the attempt fails with exit 15 and
a message naming that earlier session. A lock held by any other session is
a concurrent deploy, exit 15, as before.

`--timeout` is the wall-clock box for every attempt and every wait. The wait
before attempt n+1 is `min(30 s, 1 s * 2^(n-1))` times a random factor in
[0.8, 1.2]. A wait that would end after the deadline is not started: the run
fails at once with the last attempt's error and exit code, the message
`stopped after N attempts: the next retry would pass the <d> time box; last
error: ...` (PGMI-945). A deadline in
the middle of an attempt cancels it as before, exit 16 (11 while
connecting), with that message when an earlier attempt failed. With
`--timeout 0` the run stops after 5 attempts (`gave up after 5 attempts with
no time box (--timeout 0); last error: ...`). Each retried attempt prints
one stderr line, `pgmi: attempt N failed (<SQLSTATE> <reason>); retrying in
<wait>, <left> left` (`<k> attempts left` with no box). `--json` and the
`serve` deploy tool's `structuredContent` add `attempts` (each with
`startedAt`, `durationMs`, and the `sqlstate` and `message` of a failed one)
and `timeUsedMs`, present once an attempt started. SIGINT or SIGTERM during
an attempt or a wait is exit 130 at once. `retry: false` in `pgmi.yaml`
turns it off; there is no flag.

Cases: `retry/connection-lost-mid-run`, `retry/server-shutdown-mid-run`, `retry/connect-gave-up`, `retry/concurrency-errors`, `retry/read-only-on-a-standby`, `retry/sign-in-unavailable`, `retry/deadline-during-an-attempt`, `retry/wait-cut-at-the-deadline`, `retry/signal-during-a-wait`, `retry/failing-test-not-retried`, `retry/retry-false`, `retry/no-retry-after-commit`, `retry/orphaned-session-holds-lock`, `retry/no-time-box-five-attempts`, `exit-codes/11-connection-failed`. The table of transient failures, the backoff and the messages: the Rust unit tests of `deploy::retry`.

## Behaviour changes in 0.20

### Changed exit codes and newly refused input

Every difference above, old (Go, 0.14) against new (Rust, 0.20). "Owner" rows
are decided; the others are intended changes from the first draft. The 0.20
release notes take their "Behaviour changes" section from this table.

| Change | 0.14 (Go) | 0.20 (Rust) | Decided |
|---|---|---|---|
| Whole-deployment retry | the first failure ends the run | a transient failure (class 08, a lost connection, 57P01-57P03, 53300, 40001, 40P01, 55P03, 25006 on a hot standby, a sign-in or connect retry that gave up) runs the deploy again from the start inside `--timeout`, once an attempt has reached the server (a first attempt that never connects fails at once, as in Go), waiting 1 s doubling to 30 s; once a unit has committed the failure is final, as in Go, unless `retry_after_commit: true` in `pgmi.yaml` allows a rerun (owner 2026-11-12, PGMI-918); a unit whose reply was lost is never retried (owner 2026-11-13, PGMI-970); `retry: false` turns retrying off; `--json` adds `attempts` (each with its `unitsCommitted`) and `timeUsedMs` | owner 2026-11-12 (record 0025) |
| `sslmode=verify-ca` with no `sslrootcert` (the system trust store) | any certificate a trusted CA issued, for any host name | the host name is checked too, as in `verify-full`; with an explicit root file `verify-ca` checks no name, as before | owner 2026-11-12 (PGMI-923) |
| project `.env` that starts with a UTF-8 byte-order mark | warning, none of it applied | applied, the mark ignored | owner 2026-11-12 (PGMI-943) |
| project `.env` setting a key that locates credentials, endpoints, proxies or trust roots: `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_CONTAINER_*`, `AWS_ENDPOINT_URL*`, `AZURE_AUTHORITY_HOST`, `AZURE_FEDERATED_TOKEN_FILE`, `IDENTITY_*`, `MSI_*`, `GOOGLE_APPLICATION_CREDENTIALS`, `CLOUDSDK_*`, `DATABRICKS_CONFIG_FILE`, `DATABRICKS_HOST`, `SSL_CERT_*`, `*_PROXY` (any case) | applied | refused; the rest of the file applies and the `.env applied:` line names each refused key; the operator's own environment can still set them | owner 2026-11-12 (PGMI-921) |
| `pgmi serve`: a deploy to a server the operator did not allow (not its environment's own `host:port`, not in `PGMI_SERVE_HOSTS`) | the operator's `PGPASSWORD`, `.pgpass` and cloud sign-in are used | none of them is used (a password typed in the tool call's connection string still is); a cloud sign-in is refused, exit 10; stderr names the server; the plain CLI is unchanged | owner 2026-11-12 (PGMI-915) |
| `pgmi serve`: a deploy tool call with `overwrite: true` | runs when the call's own `confirmDatabaseName` matches | refused with exit 12 before connecting unless the operator started `pgmi serve` with `PGMI_SERVE_ALLOW_OVERWRITE=1`; then `confirmDatabaseName` must still match; the deploy tool's description says so | PGMI-924 |
| Failure summary after a stop or a retry | `nothing committed` whenever the run failed | names the units committed before a stop and those an earlier attempt committed; a unit the server never answered reads `whether it committed is unknown, check the database`; `--json` adds `outcomeUnknown` and `attempts[].unitsCommitted` | owner 2026-11-12 (PGMI-911, PGMI-912) |
| Azure tenant id outside letters, digits, `.` and `-` | refused at sign-in, exit 11 | refused before connecting, exit 10 | agent 2026-11-12 (PGMI-910) |
| A cloud sign-in endpoint that answers a redirect (pgmi's own sign-in client: Azure managed identity, Databricks, AWS, Google Cloud SQL) | followed, https to http included, and the target's answer used | not followed: the redirect is the answer, so the sign-in fails naming its status | PGMI-931 |
| `sslnegotiation=direct` to a TLS server that selects no ALPN protocol | the startup and the password are sent | refused after the handshake, before the startup message, exit 11 (libpq 17 refuses too) | PGMI-932 |
| `PGSSLCRLDIR` or `PGREQUIREPEER` set and non-empty | ignored | refused before connecting, exit 10, naming the variable (the connection-string keys `sslcrl`, `sslcrldir`, `requirepeer` were already refused); `PGSSLCRL` and `PGHOSTADDR` stay ignored, as in Go (owner 2026-11-13, PGMI-973) | PGMI-934 |
| A `.env` value that grows past 1 MiB as its variables expand (`V=$V$V` line after line) | expanded until memory runs out | refused, exit 10, naming the line and the key | PGMI-948 |
| A cloud sign-in answer larger than 1 MiB | read whole into memory | refused after 1 MiB, not retried; the sign-in fails | PGMI-930 |
| Google sign-in without Application Default Credentials | exit 1 | exit 11 | intended |
| `deploy` on a terminal with no connection from flags, environment or `pgmi.yaml` | opens the connection wizard | never interactive: exit 10, listing the ways to set a connection; `pgmi init` and `pgmi config` keep their wizards | owner 2026-11-12 (PGMI-901) |
| Connection reset with no error from the server, mid-deploy | exit 13 while deploy.sql runs, exit 1 during session setup | exit 13 once the session is open, session setup included; exit 11 only when no session opened; `--json` still shows what committed | PGMI-928 (was exit 11, PGMI-885) |
| `.env` value ending in `\"` or `\\` before the closing quote (`"x\""`) | `x\`, or reads on into later lines | `x"`, `x\` | agent 2026-11-12 (PGMI-900), owner may revisit |
| `.env` line with no key (`=x`, or a last line without `=`) | the rest applied, an empty name set | file refused, exit 10 | agent 2026-11-12 (PGMI-900), owner may revisit |
| `pgmi help <unknown topic>` | "Unknown help topic", exit 0 | exit 2 | agent 2026-11-12 (PGMI-906), owner may revisit |
| `pgmi help deploy <extra>` | extra argument ignored, exit 0 | exit 2 | agent 2026-11-12 (PGMI-906), owner may revisit |
| `pgmi completion bash` script | Cobra's first version: also offers `--flag=` forms of flags that take a value | Cobra's second version: the same words without the `--flag=` forms | agent 2026-11-12 (PGMI-905), owner may revisit |
| `--sslmode ''` | treated as unset | refused, exit 10 | agent 2026-11-12 (PGMI-906), owner may revisit |
| `.env` with a leading UTF-8 BOM | whole file refused | applied | agent 2026-11-12 (PGMI-906), owner may revisit |
| `metadata scaffold --write` on a file with a UTF-8 BOM | BOM kept before the inserted block | BOM removed | agent 2026-11-12 (PGMI-906), owner may revisit |
| `CALL pgmi_test('it''s')` (doubled quote in the pattern) | not expanded; the server answers 42883, exit 13 | expanded with pattern `it's`; no test matches, exit 13 | agent 2026-11-12 (PGMI-906), owner may revisit |
| `--json` `ignoredPaths` path of a directory | `./lib` | `./lib/`, as the serve schema says | agent 2026-11-12 (PGMI-906), owner may revisit |
| A parameter key in an error message | lowercased | as typed | agent 2026-11-12 (PGMI-906), owner may revisit |
| URI scheme not in lowercase (`POSTGRESQL://`) | refused as an unknown form, exit 10 | read as a URI | agent 2026-11-12 (PGMI-896), owner may revisit |
| `pgmi.yaml` quoted port (`port: "5433"`) | refused, exit 10 | accepted | agent 2026-11-12 (PGMI-895), owner may revisit |
| `pgmi.yaml` with two YAML documents | the second is silently ignored | refused, exit 10 | agent 2026-11-12 (PGMI-895), owner may revisit |
| `pgmi.yaml` with a `%YAML 1.2` directive | refused, exit 10 | accepted | agent 2026-11-12 (PGMI-895), owner may revisit |
| `pgmi.yaml` `port: 0` | quietly 5432 | refused, exit 10 | agent 2026-11-12 (PGMI-895), owner may revisit |
| `pgmi.yaml` negative timeout (`-5s`) | runs, then exit 11 "exceeded --timeout -5s" | refused before connecting, exit 10 | agent 2026-11-12 (PGMI-895), owner may revisit |
| A host with several addresses fails to connect | only the last address's error, retried when that one is transient | every address's error; a certificate failure among them is final, not retried; exit 11 both | intended |
| POSIX class such as `[[:space:]]` in `.pgmiignore` | refused, exit 10 | accepted | intended |
| Backslash inside a class in `.pgmiignore` (`a[\]]b`) | a literal backslash | escapes the next character, as in gitignore | owner 2026-11-13 (PGMI-947) |
| `invalid configuration:` inside an error message | `failed to scan directory: invalid configuration: .pgmiignore excludes deploy.sql ...` | the same message without the infix; exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| Driver error text in connection and retry lines | pgx's text, sorted by pgmi (`server at h:p refused the connection: ...`, `password authentication failed for user "u" on database "d"`, `SSL/TLS connection error: ...`) | each host as `h:p: ` with the server's message and SQLSTATE, or the TLS or driver library's text; the next-step lines and exit codes unchanged | agent 2026-11-12 (PGMI-907), owner may revisit |
| Azure, AWS and Google sign-in failure text | the Go SDKs' wording, credential chain and troubleshooting links | pgmi's own sign-in chain: one line per credential tried; `connect to "<db>": ` and the exit codes unchanged | agent 2026-11-12 (PGMI-907), owner may revisit |
| Help layout (`--help`, `help <command>`) | cobra's: `Usage:` on its own line, `Flags:` with `[=true|false]` and a type word, examples after the usage | clap's: `Usage: pgmi ...`, `Arguments:`, `Options:` with `[default: ...]`, examples last; the descriptions and examples are Go's, except `pgmi help help`, reworded without "simply" | agent 2026-11-12 (PGMI-907, PGMI-908), owner may revisit |
| Session-preparation failure | `session preparation failed: execution failed: failed to create internal tables: failed to create pgmi schema: ...; pgmi's session tables need the TEMPORARY privilege: GRANT ...` | `session preparation failed: create the session tables: ...`, the `GRANT` on its own line | agent 2026-11-12 (PGMI-907), owner may revisit |
| Input refused before connecting | `FAILED <db>: failed after ...`, then the error (a connection option value, a client certificate without its key, AWS sign-in without a region) | the error alone; exit 10 both (a refused parameter key or value prints the FAILED line in both) | agent 2026-11-12 (PGMI-907), owner may revisit |
| `pgmi.yaml` that is not valid YAML or has an unknown key | yaml.v3's text (`yaml: unmarshal errors: line 2: field x not found in type config.ProjectConfig`) | the line, column and parser text, naming the keys expected (`line 2 column 3: unknown field ...`); exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| A project path that does not exist | `failed to scan directory: failed to open directory: failed to access path: stat <path>: no such file or directory` | `project path "<path>" does not exist`; exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| `PGMI_MAX_FILE_SIZE` that is not a positive whole number | reported inside the first file read (`failed to read ./x.sql: PGMI_MAX_FILE_SIZE=...`) | reported alone; exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| A `.pgmiignore` pattern that cannot be read | Go regexp's reason (`error parsing regexp: ...`, `empty character class`) | `invalid pattern "<p>"`; exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| `--json` `filesLoaded` when connecting fails | 0 | the files loaded before connecting | agent 2026-11-12 (PGMI-907), owner may revisit |
| Refusal order: a bad `pgmi.yaml` timeout and no database | the missing database | the timeout; exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| Connection source named after refusing a flag beside an environment connection string | `(connection defaults)`, or none | `(connection from DATABASE_URL)` (the variable that clashed); exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| `--sslmode` value that is not a mode | the error, then `(connection defaults)` | the error alone; exit 10 both | agent 2026-11-12 (PGMI-907), owner may revisit |
| `metadata scaffold` project-root search | walks past `.git` | stops at `.git`, prints `Project root:` | intended |
| Azure service principal with a certificate | not reachable | signs in | owner 2026-11-10 |
| `sslrootcert=system` with an explicit weaker sslmode | upgraded to `verify-full` | newly refused, exit 10 | owner 2026-11-11 |
| Connection-string parse errors | echo a keyword, ADO.NET part or port | never quote the input; exit codes unchanged | owner 2026-11-11 |
| `.pgmiignore` that exists but cannot be read | exit 1 | exit 10 | owner 2026-11-11 |
| Project file whose name is not UTF-8 | exit 13, after the target database was created | newly refused before connecting, exit 10 | owner 2026-11-11 |
| `sslcrl`, `sslcrldir`, `requirepeer`, `sslcertmode=require` | exit 11 (the server refuses the setting) | newly refused before connecting, exit 10 | owner 2026-11-11 |
| `ssl_min_protocol_version`, `ssl_max_protocol_version`, `sslsni=0`, `sslcertmode=disable` | exit 11 (the server refuses the setting) | honoured; an unknown version or an empty range exit 10 | intended |
| Client private key that group or others can read | used | newly refused, exit 10 | owner 2026-11-11 |
| Encrypted client key whose PBKDF2 or PKCS#12 iteration count is above 1,000,000, or whose scrypt N·r·p is above 2^23 (openssl writes 2048 and 2^17) | derived, however long it takes | newly refused, exit 10, naming the file (Rust unit tests `a_key_whose_encryption_would_run_for_hours_is_refused_by_name`, `keys_openssl_writes_open_and_a_runaway_cost_is_refused`) | owner 2026-11-11 |
| Windows: files under `%APPDATA%\postgresql` | read, and `pgmi config` saves the password there | unchanged | no change |
| `min_protocol_version=3.2` or `latest` | protocol 3.2 | newly refused before connecting, exit 10 (known gap, PGMI-865) | owner 2026-11-11 |
| SIGHUP (a closed terminal or SSH session) or a stderr reader that went away, during `deploy` | the process dies at once; the statement runs on and keeps the deploy lock | SIGHUP is handled as SIGTERM (statement cancelled, exit 130); writes to a closed pipe are dropped and the deploy goes on | PGMI-946 |
| Two `<pgmi-meta>` elements in one comment | the first used, the rest ignored | newly refused, exit 10 | owner 2026-11-11 |
| A `<pgmi-meta>` element with an attribute given twice (two `id`s) | the last value used | refused, exit 10 | owner 2026-11-12 (PGMI-942) |
| `serve`: a line over 16 MiB | read | newly refused with `-32600`, session goes on | owner 2026-11-11 |
| A `--params-file` (or `paramsFile`, or `-`) over `PGMI_MAX_FILE_SIZE` (10 MiB) | read whole | newly refused, exit 10 (`exceeds N-byte limit`) | intended |
| A project `.env` over `PGMI_MAX_FILE_SIZE` | applied | newly refused, exit 10, naming the file | intended |
| A password file (`.pgpass`, `PGPASSFILE`, `passfile`) over 10 MiB | read | not read, with a WARNING naming it, as a world-readable one is | intended |
| `serve`: a `tools/call` while 32 calls run | runs | newly refused with `-32000` | owner 2026-11-11 |
| `serve`: 1024 answers the client has not read | queued | the session ends | owner 2026-11-11 |
| `serve`: a second SIGINT or SIGTERM while answers are written | ignored, answers drained, exit 0 | ends at once, exit 130 | owner 2026-11-12 |
| `serve`: a line holding only `null` | no answer | `-32600` with a null id | owner 2026-11-11 |
| `serve`: an id holding invalid UTF-8 | echoed as raw bytes (invalid JSON) | echoed with U+FFFD | owner 2026-11-11 |
| `serve`: a `tools/call` whose id is still running | both run, two answers | second refused with `-32600` | owner 2026-11-11 |
| `serve`: a cancel or a repeated id spelled with escapes (`"\u006aob"` for `"job"`) | missed | matched by the decoded string | owner 2026-11-11 |
| Azure username/password (resource-owner) login | not reachable | removed | owner 2026-11-10 |
| `pgmi init`: `sslmode` in the scaffolded `pgmi.yaml` | `prefer`, refused with token sign-in | not written; the default applies (`prefer`, or TLS only with token sign-in) | owner 2026-11-11 |
| Token sign-in refusing `sslmode: prefer` from `pgmi.yaml` | names the source | names the file line and the fix; exit 10 both | owner 2026-11-11 |
| Basic template's privilege test | fails on any PUBLIC-executable function in `public`, also a hosting provider's | checks only the deploying role's functions | owner 2026-11-11 |
| Known secrets (password, `sslpassword`, sign-in tokens) in notices, errors, `--json`, serve | printed | shown as `***`, also where the secret is part of a longer word; a password equal to the login or a database name is not hidden, since pgmi prints those names anyway (owner 2026-11-13, PGMI-973) | owner 2026-11-11 |
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
| Repeated URI query key (`?sslmode=disable&sslmode=require`) | the first value | the last value, as libpq | agent 2026-11-12 (PGMI-897), owner may revisit |
| Unknown connection-string option written after a password | named in the refusal | not named (it may be the password's rest); exit 10 both | owner 2026-11-11 (never quote the input) |
| `connect_timeout` above 9223372036 seconds | wrapped around (a negative or short timeout) | newly refused before connecting, exit 10 | intended |
| `connect_timeout` with a stalled host lookup or sign-in | waits for it (pgx counts only the connection) | counted per attempt; the next host is tried | intended |
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
| A NUL byte in the project `.env` | in a key: nothing applied, WARNING; in a value: that pair dropped, the rest applied and listed | nothing applied, WARNING; exit 0 both, the value never printed | intended |
| `pgmi init` into a directory that exists but cannot be listed | exit 1 both (Rust 0.20 alphas: files overwritten) | exit 1, nothing written; files created exclusively | intended |
| TLS or cloud sign-in on an x86-64 CPU without AES-NI, PCLMULQDQ, SSSE3, AVX, AVX2, BMI1, BMI2, ADX (before about 2014), or an arm64 CPU without NEON, AES, PMULL, SHA2 | works | newly refused before connecting, exit 10, naming the missing features; `sslmode=disable` and commands without a database still run (record 0021) | owner 2026-11-11 |
| `--help` of a binary run under another file name | `Usage:` names `pgmi` | names the file it was run as, as clap prints it; usage errors say `pgmi` in both (the differential run names both binaries `pgmi`) | owner 2026-11-13 (PGMI-973): wording only |
| A command-line argument pgmi reads as text that is not UTF-8 (`-d $'\xff'`) | passed on, fails later (exit 11 or 1) | refused at once, exit 2 (`invalid UTF-8 was detected in one or more arguments`) | owner 2026-11-13 (PGMI-973): a usage error is known before anything runs |
| `--timeout` with a negative value (`-1s`) | runs, then exit 11 `exceeded --timeout -1s` | refused, exit 2 (`must not be negative`), as `pgmi.yaml` is | owner 2026-11-13 (PGMI-973): better, a typo is not a timeout |
| A write to stdout that fails (`> /dev/full`) | ignored, exit 0 | `write output: no space left on device`, exit 1 | owner 2026-11-13 (PGMI-973): better, lost output is a failure |
| `metadata scaffold <file>` | scans as if given a directory | refused, exit 10 (`"<file>" is a file; pass the project directory`) | owner 2026-11-13 (PGMI-973): better, the command takes a project |
| `CALL pgmi_test('pattern', NULL)` | not expanded; the server answers 42883, exit 13 | expanded with the default callback, as the contract's macro section says | owner 2026-11-13 (PGMI-973): better |
| Session SQL against a `search_path` that puts a user schema first (a `public` function shadowing a catalog one) | pgmi's session queries can call the user's function | they call `pg_catalog` objects by qualified name (PGMI-917) | owner 2026-11-13 (PGMI-973): better |
| `pgmi.yaml` timeout that is not a duration, with a `--timeout` flag | the flag wins, the bad value is ignored | refused, exit 10 (`invalid timeout in pgmi.yaml`) | owner 2026-11-13 (PGMI-973): a broken file is reported whatever overrides it |
| A `<pgmi-meta>` block with more than one fault (a bad id and a bad `idempotent`, a bad id and broken XML, an empty id) | the id first (`id attribute is not a UUID`) | the first fault in the Rust parser's order (`idempotent`, the XML, an empty id as `id attribute is required`); exit 10 both | owner 2026-11-13 (PGMI-973): wording only |
| `CALL pgmi_test('<pattern>')` with a pattern that is not a valid regular expression | `failed to preprocess deploy.sql: execution failed: failed to call pgmi_test_generate: ERROR: Invalid regex pattern ...` | the server's `ERROR: Invalid regex pattern ...` alone; exit 13 both | owner 2026-11-13 (PGMI-973): wording only |
| `pgmi init <dir>` into a directory that is not empty | names the absolute path | names the path as typed; exit 2 both | owner 2026-11-13 (PGMI-973): wording only |
| `PGMI_PARAM_<NAME>` whose value is not UTF-8 | `FAILED <db>: ...`, then `invalid parameter value for '<name>': not valid UTF-8` | `environment variable PGMI_PARAM_<NAME> is not valid UTF-8`, the error alone; exit 10 both | owner 2026-11-13 (PGMI-973): wording only, it names the variable to fix |
| `connect_timeout` or `sslmode` in a connection string that is not a valid value | `invalid connection string: invalid connect_timeout [value] "x": strconv.Atoi: ...`; `invalid sslmode "x" in the connection (valid: disable, allow, ...)` | `invalid connect_timeout "x"`; `invalid sslmode value: "x"`, libpq's words; exit 10 both | owner 2026-11-13 (PGMI-973): wording only |
| A failure to connect to the maintenance database | `maintenance database: connect to "<db>": ...` | `connect to "<db>": ...`; exit codes unchanged | owner 2026-11-13 (PGMI-973): wording only, the line names the database |
| Refusal order: a `pgmi.yaml` port that is not a number, or a quoted one, and no database | the port (`cannot unmarshal !!str ... into int`) | the missing database; with a database, `invalid port "x"` (a quoted number is accepted, row above); exit 10 both | owner 2026-11-13 (PGMI-973): wording only |
| A database name holding `"` or `\` in the `--overwrite` lines | Go-escaped (`"o2\"x"`) | as written between double quotes | owner 2026-11-13 (PGMI-973): wording only |

No case of its own: each row's section above names its cases or its Rust unit
test.
