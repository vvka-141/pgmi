# Black-box suite

Runs a pgmi binary from outside, the way a user does, and checks what it does:
exit code, output, and what ends up in PostgreSQL. It imports nothing from
pgmi, so the same cases test the Go binary today and the Rust binary later.

```bash
make blackbox                                  # build ./cmd/pgmi, start postgres:18 in Docker
PGMI_BIN=/path/to/pgmi make blackbox           # any binary
PGMI_TEST_CONN=postgresql://postgres:postgres@127.0.0.1:5432/postgres make blackbox
cd blackbox && uv run pytest -k 13-sql         # one case
```

Each case gets its own database and an empty `HOME`, and runs with only
`PATH`, `HOME`, `LANG` and the case's own `env`: no `PG*` variable or
`~/.pgpass` from your shell reaches the binary. The database and any role whose
name starts with it are dropped afterwards.

## A case

One YAML file under `cases/<area>/`. The file name is the test name.

```yaml
description: a failing statement is exit 13 and the head commits nothing
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
steps:
  - pgmi: [deploy, "{project}", --connection, "{conn}", -d, "{db}"]
    env: {PGMI_PARAM_TOKEN: abc}   # optional
    stdin: "a=b\n"                 # optional; or a list of lines and {wait: seconds}
    cwd: "{project}"               # optional: working directory (default {workdir})
    delay: 0                       # optional: seconds to wait before starting
    background: false              # optional: true checks it after the last step
    signal: {name: SIGINT, after: 3}  # optional; or a list, each `after` counted from the start
    timeout: 120                   # optional: seconds before the step fails
    expect:
      exit: 13                     # required for a pgmi step
      stdout: [regex, ...]         # each must match
      stdout_not: [regex, ...]     # none may match
      stderr: ["division by zero"]
      stderr_not: []
      stderr_within:               # the line arrived within that many seconds of the start
        - {pattern: early-notice, seconds: 2.5}
      json: {status: failed}       # stdout parsed as JSON; these keys must match
      sql:                         # run against {db} after the step
        - query: SELECT count(*) FROM pg_class WHERE relname = 'marker'
          rows: [[0]]
      files:                       # paths relative to {workdir}; each regex must match
        project/deploy.sql: ["1/0"]
      absent: [project/out.txt]    # paths that must not exist
  - sql: SELECT pg_advisory_lock(1)   # a step can also run SQL against {db}
```

Placeholders, replaced in arguments, `env` values, `stdin`, `cwd`, file contents,
SQL, expected rows, `files` paths and `json` strings: `{project}`, `{db}`, `{conn}`
(URI of the maintenance database), `{host}`, `{port}`, `{user}`, `{password}`, `{home}`, `{workdir}`,
and `{silent}` (a `host:port` that accepts connections and never answers).

## Extra servers

`servers:` starts each kind once per run, in Docker, next to the main server.
Databases a case creates there are dropped afterwards.

| Kind | What | Placeholders |
|---|---|---|
| `tls` | TLS on, certificate for `localhost` only (so `127.0.0.1` fails verify-full), signed by a throwaway CA; `{cert_user}` must present a client certificate; `{md5_user}` signs in with md5 | `{tls_port}`, `{ca}`, `{wrong_ca}`, `{client_cert}`, `{client_key}`, `{client_key_encrypted}`, `{key_password}`, `{ca_pem}`, `{wrong_ca_pem}`, `{client_cert_pem}`, `{client_key_pem}`, `{cert_user}`, `{md5_user}` |
| `tls_only` | refuses connections without TLS | `{tls_only_port}` (and the certificate placeholders) |
| `replica` | a primary and a hot standby streaming from it | `{primary_port}`, `{standby_port}` |
| `socket` | its Unix socket directory is shared with the host | `{socket_dir}` |

The certificates come from `openssl`; Docker and `openssl` must be on `PATH`.

`COVERAGE.md` lists every behaviour and the case that checks it. A new case
updates its row.

A case is data. When it cannot say something, add the feature to the runner
(`pgmi_blackbox/`), not Python to the case. `uv run pytest selftest` checks the
runner's schema validation.
