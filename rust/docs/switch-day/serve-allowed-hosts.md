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

Google sign-in is the exception to "server": pgmi then connects to the
Cloud SQL instance the project's `pgmi.yaml` names (`google_instance`), at
the address Google's API gives for it, not to a host. So for Google the
operator allows instances: `PGMI_SERVE_CLOUDSQL_INSTANCES` is a
comma-separated list of instance connection names
(`project:region:instance`), read when `pgmi serve` starts and compared
exactly. A deploy whose `pgmi.yaml` names any other instance is refused with
exit 10 before any sign-in.

On an allowed server the operator's credentials go only under the
operator's own trust settings. Four settings are enforced:

- `sslmode` and `channel_binding` of a tool call are raised to the
  operator's (disable < allow < prefer < require < verify-ca < verify-full;
  disable < prefer < require).
- `require_auth` becomes the operator's when the call set none; one that
  allows a method the operator's does not is refused, exit 10.
- `sslrootcert` must be the operator's own, because the root certificate
  decides which server passes verification. A call whose connection string,
  or whose project's `pgmi.yaml`, names another one (or `system`, or an
  empty one) is refused with exit 10 before connecting: `sslrootcert "..."
  is not the operator's own (...)`. Set it in the environment `pgmi serve`
  runs in instead.

Other TLS settings of a call (`sslnegotiation`, `sslsni`, the protocol
version bounds, its own `sslcert` and `sslkey`) are taken as written: they
are not among the operator's rules, and a call that leaves them out gets
pgmi's defaults.

A tool call that types its own password is not using the operator's
password, so it keeps all of its own settings. A cloud sign-in is the
exception: with `auth_method` in the project's `pgmi.yaml` the token is what
the server gets, whatever password the call types, so the four rules apply
to that call too.

The rules are the operator's own settings, so `pgmi serve` does not start
(exit 10) when its environment does not resolve to connection settings, for
example an `sslmode` or `require_auth` that is no such value, or
`PGCLIENTENCODING=auto`. The message names the setting.

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
directories too. A params file is read only for a project
directory that holds a `deploy.sql` (exit 14 otherwise), so a call cannot
name `/` as its project to probe other files. The `init` tool writes only
inside `PGMI_SERVE_ROOTS` when it is set, and never into a directory that
holds a symbolic link. The `metadata_plan` and `metadata_validate` tools
read only inside `PGMI_SERVE_ROOTS` when it is set. A server that is not
allowed gets no client certificate of the operator either, unless the call
names its own `sslcert` and `sslkey`, both non-empty. With `PGMI_SERVE_ROOTS` unset there is no directory limit: a tool
call may deploy any pgmi project, and scaffold into any empty directory, the
account can reach.

```json
{"mcpServers": {"pgmi": {"command": "pgmi", "args": ["serve"],
  "env": {"PGMI_SERVE_HOSTS": "db.internal,staging.example.com:6432",
          "PGMI_SERVE_CLOUDSQL_INSTANCES": "my-project:europe-west4:main"}}}}
```

The plain `pgmi deploy` command is not affected.
