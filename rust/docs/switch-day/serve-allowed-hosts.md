# pgmi serve: which servers get the operator's credentials

An MCP client is an agent, and an agent can be talked into naming a server.
`pgmi serve` therefore sends the credentials of the account it runs as only
to servers that account chose:

- the `host:port` its own environment resolves to (`PGHOST`/`PGPORT`,
  `PGMI_CONNECTION_STRING`, the defaults; `PGDATABASE` need not be set), and
- every entry of `PGMI_SERVE_HOSTS`, a comma-separated list of `host` (any
  port), `host:port`, `[ipv6]` or `[ipv6]:port`, read when `pgmi serve`
  starts. An IPv6 address must be in brackets: an entry such as `::1:5432`
  is refused at start with exit 10, since it is itself an address. Host
  names compare without case, addresses by value, ports exactly.

For any other server a deploy tool call still runs, but pgmi uses neither
`PGPASSWORD`, nor a password file (`passfile`, `PGPASSFILE`, `~/.pgpass`),
nor a cloud sign-in (`--azure`, `--aws`, `--google`, `--databricks`, or
`auth_method` in the project's `pgmi.yaml`; a cloud sign-in is refused with
exit 10). A password the tool call types in its own connection string is
used. stderr names the server that was not allowed, and when the server
then asks for a password, the tool result says the operator's password was
not used for that host because it is not on the list.

On an allowed server the operator's credentials go only with settings at
least as strict as the operator's own: a tool call's `sslmode` and
`channel_binding` are raised to the operator's (disable < allow < prefer <
require < verify-ca < verify-full; disable < prefer < require), and its
`require_auth` becomes the operator's when it set none. A `require_auth`
that allows a method the operator's does not is refused, exit 10. A tool
call that types its own password keeps its own settings.

A tool call may not set `hostaddr` or `passfile` in its connection string.

A deploy tool call with `overwrite: true` drops the database, so it is
refused (exit 12, before connecting) unless the operator starts `pgmi serve`
with `PGMI_SERVE_ALLOW_OVERWRITE=1`. With it, the call must still repeat the
database name in `confirmDatabaseName`.

A `paramsFile` of a deploy tool call must lie inside the project, or inside
a directory of `PGMI_SERVE_ROOTS`, a comma-separated list read when `pgmi
serve` starts; any other file is refused with exit 10 before connecting, so
a call cannot load an arbitrary `KEY=VALUE` file into the session. When
`PGMI_SERVE_ROOTS` is set, the project path must lie inside one of its
directories too.

```json
{"mcpServers": {"pgmi": {"command": "pgmi", "args": ["serve"],
  "env": {"PGMI_SERVE_HOSTS": "db.internal,staging.example.com:6432"}}}}
```

The plain `pgmi deploy` command is not affected.
