# Black-box suite

Runs a pgmi binary from outside, the way a user does, and checks what it does:
exit code, output, and what ends up in PostgreSQL. It imports nothing from
pgmi, so the same cases test the Go binary today and the Rust binary later.

```bash
make blackbox                                  # build ./cmd/pgmi, start postgres:18 in Docker
PGMI_BIN=/path/to/pgmi make blackbox           # any binary
PGMI_REFERENCE_BIN=/path/to/go-pgmi make blackbox  # binary for steps marked binary: reference
PGMI_TEST_CONN=postgresql://postgres:postgres@127.0.0.1:5432/postgres make blackbox
PGMI_IMPL=rust PGMI_BIN=$PWD/target/release/pgmi make blackbox  # Rust expectations
cd blackbox && uv run pytest -k 13-sql         # one case
```

Each case gets its own database and an empty `HOME`, and runs with only
`PATH`, `HOME`, `LANG` and the case's own `env`: no `PG*` variable or
`~/.pgpass` from your shell reaches the binary. The database and any role whose
name starts with it are dropped afterwards.

A case with `init: advanced` is skipped, with the reason, on a server older
than PostgreSQL 18, the advanced template's floor; CI runs the Rust passing
list on postgres:15 too, pgmi's own floor.

## A case

One YAML file under `cases/<area>/`. The file name is the test name.

```yaml
description: a failing statement is exit 13 and the head commits nothing
impl: rust               # optional: run only for this implementation (see below)
servers: [tls]           # optional: extra servers the runner starts (see below)
home:                    # optional: files written into the case's empty HOME
  .pgpass: "{host}:{port}:*:{user}:{password}\n"     # mode 0600
  .postgresql/root.crt: {content: "{ca_pem}", mode: "0644"}
project:
  init: basic            # optional: pgmi init -t minimal|basic|advanced first
  generate: {count: 3000, path: "gen/f{i}.sql", content: "SELECT {i};"}  # optional
  files:                 # optional: files written into the project (after init)
    deploy.sql: |
      CREATE TABLE marker (id int);
      SELECT 1/0;
    data.bin: {hex: "610062"}        # raw bytes
    link.sql: {symlink: ../x.sql}    # a symbolic link
    pipe.sql: {fifo: true}           # a named pipe
    locked: {dir: "0300"}            # a directory with this mode, set after every file (restored at the end)
steps:
  - pgmi: [deploy, "{project}", --connection, "{conn}", -d, "{db}"]
    env: {PGMI_PARAM_TOKEN: abc}   # optional
    stdin: "a=b\n"                 # optional; or a list of lines, {wait: seconds},
                                   # {until: regex, timeout: 30} (holds the next line until stdout matches; not with tty),
                                   # {text: "...", times: n} and {hex: "ff0a", times: n} (no newline added)
    cwd: "{project}"               # optional: working directory (default {workdir})
    delay: 0                       # optional: seconds to wait before starting
    background: false              # optional: true checks it after the last step
    signal: {name: SIGINT, after: 3}  # optional, tty too; or a list, each `after` counted from the start
    timeout: 120                   # optional: seconds before the step fails
    tty: true                      # optional: run in a 120x40 pseudo-terminal; stdin is typed as keys
    read_rate: 8000                # optional: read stdout at this many bytes a second (not with tty)
    binary: reference              # optional: run PGMI_REFERENCE_BIN instead of PGMI_BIN
    expect:
      exit: 13                     # required for a pgmi step
      stdout: [regex, ...]         # each must match
      stdout_not: [regex, ...]     # none may match
      stderr: ["division by zero"]
      stderr_not: []
      raw: ["\\x1b\\[[0-9;]*m"]     # tty only: regexes on the output before escape codes are stripped
      raw_not: []
      stderr_within:               # the line arrived within that many seconds of the start
        - {pattern: early-notice, seconds: 2.5}
      json: {status: failed}       # stdout parsed as JSON; these keys must match
      sql:                         # run against {db} after the step
        - query: SELECT count(*) FROM pg_class WHERE relname = 'marker'
          rows: [[0]]
      files:                       # paths relative to {workdir}; each regex must match
        project/deploy.sql: ["1/0"]
      absent: [project/out.txt]    # paths that must not exist
      rust: {exit: 10, stderr: []} # optional: these keys replace the ones above under PGMI_IMPL=rust
  - sql: SELECT pg_advisory_lock(1)   # a step can also run SQL against {db}
  - sql: CREATE DATABASE "{db}" ENCODING 'LATIN1' TEMPLATE template0 LC_COLLATE 'C' LC_CTYPE 'C'
    database: maintenance             # or against the maintenance database ({db} does not exist yet)
```

`PGMI_IMPL` (`go` or `rust`, default `go`; any other value fails the run)
says which binary `PGMI_BIN` is. Under `rust`, a step's `expect.rust` keys
replace the base keys of the same name; keys it does not set stay. Use it only
where the owner decided the Rust binary differs, and name that section of
`docs/design/behaviour-contract.md` in the case's description.

`impl: rust` (or `go`) runs a whole case for that implementation only; under
the other one it is skipped. It is for new scope the other binary does not
have, such as Rust's `--databricks`, never for a difference in behaviour
(that is `expect.rust`). Say "Rust only" and name the contract section in the
description.

Placeholders, replaced in arguments, `env` values, `stdin`, `cwd`, file contents,
SQL, expected rows, `files` paths, `stdout`/`stderr`/`raw`/`files` regexes (escaped) and `json` strings: `{project}`, `{db}`, `{conn}`
(URI of the maintenance database), `{host}`, `{port}`, `{user}`, `{password}`, `{home}`, `{workdir}`,
and `{silent}` (a `host:port` that accepts connections and never answers).

## Extra servers

`servers:` starts each kind once per run, in Docker, next to the main server.
Databases a case creates there are dropped afterwards.

| Kind | What | Placeholders |
|---|---|---|
| `tls` | TLS on, certificate for `localhost` only (so `127.0.0.1` fails verify-full), signed by a throwaway CA; `{cert_user}` must present a client certificate; `{md5_user}` signs in with md5 | `{tls_port}`, `{ca}`, `{wrong_ca}`, `{client_cert}`, `{client_key}`, `{client_key_encrypted}`, `{client_key_pbes2}`, `{client_key_pbe_sha1_3des}`, `{key_password}`, `{ca_pem}`, `{wrong_ca_pem}`, `{client_cert_pem}`, `{client_key_pem}`, `{cert_user}`, `{md5_user}` |
| `tls_only` | refuses connections without TLS | `{tls_only_port}` (and the certificate placeholders) |
| `tls_self_signed` | TLS only, with a self-signed `CA:TRUE` certificate for `localhost` made as the PostgreSQL docs show (`openssl req -new -x509`) | `{tls_self_signed_port}`, `{self_signed}` (and the certificate placeholders) |
| `replica` | a primary and a hot standby streaming from it | `{primary_port}`, `{standby_port}` |
| `socket` | its Unix socket directory is shared with the host | `{socket_dir}` |
| `fakes` | in-process: an HTTP and an HTTPS server answering token requests (Azure, AWS, Cloud SQL, and a Databricks workspace: `/oidc/v1/token` and `/api/2.0/postgres/credentials`), and a PostgreSQL front that records the password it is sent (then refuses it) | `{fake_http}`, `{fake_https}`, `{fake_http_log}`, `{fake_https_log}`, `{recorder_port}`, `{recorder_log}`, `{azure_token}`, `{lakebase_token}`, `{databricks_pat}`, `{databricks_client_id}`, `{databricks_client_secret}`, `{databricks_cli_token}`; the `tls` server has `{azure_user}` with the Azure token and `{lakebase_user}` with the Lakebase credential as password. The workspace refuses an unknown bearer (401), answers 403 and 503 for `{databricks_pat}-403` and `-503`, 404 for an endpoint ending in `/endpoints/missing`, and a 60-second credential for one ending in `-short` |

The certificates come from `openssl`; Docker and `openssl` must be on `PATH`.

A `/flaky/<code>x<n>/` prefix on a fake endpoint's path answers `<code>` to
its first `n` requests, then serves the rest of the path.

## Fault injection

`proxy:` puts a TCP proxy (`pgmi_blackbox/proxy.py`) in front of the main
server for the whole case; reach it with `{proxy_conn}` (URI of the maintenance
database, `sslmode=disable`) or `{proxy_port}`. It logs every connection,
startup, query and fault to `{proxy_log}`. Each fault is one rule; the first
rule that matches an event acts:

```yaml
proxy:
  - {at: startup, do: reset, times: 3, step: 1}       # the first 3 startups during steps[1]
  - {at: startup, do: {error: "53300"}}               # FATAL with that SQLSTATE, then close
  - {query: head_sleep, do: {error: "57P01"}, after: 0.5}  # forward, 0.5 s later end the session
  - {query: INSERT, do: {error: "25006", severity: ERROR}} # answer in the server's place, session goes on
  - {query: pg_sleep, do: freeze, after: 0.5}         # stop answering anything, cancel requests included
  - {query: pg_sleep, do: cut-client, after: 0.5}     # RST the client only; the server's socket stays open
  - {query: "^CREATE DATABASE", do: {delay: 6}}       # hold that query 6 s on its way to the server
  - {at: close, do: {delay: 3}}                       # the client's close reaches the server 3 s late
```

`at` is `accept`, `startup` or `close`; `query` is a regex on the text of a
Query or Parse message (the two are exclusive). `do` is `reset` (RST both
sides), `cut-client` (RST the client; the server never learns), `freeze`, `{delay: s}` or `{error: SQLSTATE, severity, message}`
(`FATAL` by default; `ERROR` only for a query, not with `after`). Without
`after` the fault acts instead of forwarding the message; with it the message
goes through first. `times` limits a rule to its first matches; `step` to one
step. The proxy answers an SSLRequest with `N` itself.

A server stays busy after its client vanishes until its statement ends: a
case that resets a running statement sets `client_connection_check_interval`
in `deploy.sql` so the orphan ends at once.

`COVERAGE.md` lists every behaviour and the case that checks it. A new case
updates its row.

A case is data. When it cannot say something, add the feature to the runner
(`pgmi_blackbox/`), not Python to the case. `uv run pytest selftest` checks the
runner's schema validation.

## Differential run

`test_differential.py` runs the Go and the Rust binary on the same input, each
in its own workspace and database, and fails on every difference nothing
explains. It is not in the default run.

```bash
cd blackbox
PGMI_BIN=~/go/bin/pgmi PGMI_BIN_OTHER=$PWD/../target/release/pgmi uv run pytest test_differential.py
PGMI_DIFF_EXAMPLES=200 ...                     # more generated variants (default 20 per kind)
```

Inputs are every case (its `pgmi` steps, run on each binary in turn) and
variants `hypothesis` generates with a fixed seed: connection strings,
`pgmi.yaml` files and project trees under `.pgmiignore` patterns. Per step it
compares the exit code, stdout, stderr, the `--json` record and the target
database's catalog (schemas, relations, columns, functions, ACLs, roles named
after the database, row counts). Only the workspace paths, the database name,
durations, each binary's version and build line, UUIDs, the server's process
ids and OIDs (`process 121`, `relation 16862`), the row count of a table
bucketed by the minute (the advanced template's `api.access_denial`, which two
runs fill across a different number of minutes), the volatile `--json` keys and
the order of `serve` answers (they answer concurrently) are normalised. JSON on
stdout, a whole document or one per line, is compared as data: key order and
string escapes do not count.

A difference is accepted when:

- the case's `expect.rust` sets that field for that step (cited as the case), or
- an `accept` entry in `differential-allow.yaml` matches it: `row` is the exact
  first cell of a row (or a `###` heading) of "Behaviour changes in 0.20" in
  `docs/design/behaviour-contract.md` and is checked against it; `case` is a
  regex for the whole case path or variant name, `field` one of `exit`,
  `stdout`, `stderr`, `json`, `db`, `error`; optional `step`; and at least one
  of `go`/`rust`, a regex the output must contain. Keep entries narrow.
- after the rewrites of the entries that match it, the two outputs are equal:
  an entry with `go_as` (or `rust_as`) replaces what its `go` (or `rust`)
  pattern matches with that text instead of accepting the whole difference, so
  it explains one wording change and nothing else in the same output.

`skip` in the same file names cases a lockstep comparison cannot judge, each
with its reason. Never edit a case's expectations to quiet the harness.
