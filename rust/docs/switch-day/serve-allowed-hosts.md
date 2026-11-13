# pgmi serve: which servers get the operator's credentials

An MCP client is an agent, and an agent can be talked into naming a server.
`pgmi serve` therefore sends the credentials of the account it runs as only
to servers that account chose:

- the `host:port` its own environment resolves to (`PGHOST`/`PGPORT`,
  `PGMI_CONNECTION_STRING`, the defaults), and
- every entry of `PGMI_SERVE_HOSTS`, a comma-separated list of `host` (any
  port) or `host:port`, read when `pgmi serve` starts.

For any other server a deploy tool call still runs, but pgmi uses neither
`PGPASSWORD`, nor a password file (`passfile`, `PGPASSFILE`, `~/.pgpass`),
nor a cloud sign-in (`--azure`, `--aws`, `--google`, `--databricks`, or
`auth_method` in the project's `pgmi.yaml`; a cloud sign-in is refused with
exit 10). A password the tool call types in its own connection string is
used. stderr names the server that was not allowed.

A tool call may not set `hostaddr` or `passfile` in its connection string.

A deploy tool call with `overwrite: true` drops the database, so it is
refused (exit 12, before connecting) unless the operator starts `pgmi serve`
with `PGMI_SERVE_ALLOW_OVERWRITE=1`. With it, the call must still repeat the
database name in `confirmDatabaseName`.

```json
{"mcpServers": {"pgmi": {"command": "pgmi", "args": ["serve"],
  "env": {"PGMI_SERVE_HOSTS": "db.internal,staging.example.com:6432"}}}}
```

The plain `pgmi deploy` command is not affected.
