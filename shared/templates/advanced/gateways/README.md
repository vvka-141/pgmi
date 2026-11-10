# Reference gateways

Working examples of a gateway in front of this database: the web server that
signs people in, opens one transaction per request and calls the `api.*`
entry points. pgmi never deploys this folder (`.pgmiignore`).

**Do not copy an example into the project as is.** Read the one in your
language, then write your own gateway or service that fits this project:
its framework, its configuration, its names. Every place that needs a
project decision is marked `ADAPT`.

They are examples to port, not packages to depend on.

Read [PROTOCOL.md](PROTOCOL.md) first: the wire rules every gateway keeps, in
any language. It describes what this database expects, so treat it as
read-only; document your own interceptors and settings in your gateway's
README.

| Folder | Stack | Serves | Tests |
|---|---|---|---|
| [python/](python/) | standard library `http.server`, psycopg 3 with psycopg_pool | REST under `/api`, MCP on `/mcp` with the OAuth resource server and rate limits (`resource_server.py`), OIDC sign-in (`login_transport.py`), dev sign-in; both seams ([python/README.md](python/README.md)) | `python -m unittest test_gateway` |
| [typescript/](typescript/) | Node `node:http`, node-postgres `Pool`, `jose` | REST under `/api`, MCP on `/mcp` with the OAuth resource server and rate limits (`resource-server.ts`), OIDC sign-in (`login.ts`), dev sign-in; both seams ([typescript/README.md](typescript/README.md)) | `npm ci && npm test` |
| [go/](go/) | `net/http`, pgx v5 `pgxpool`, go-oidc, x/oauth2 | REST under `/api`, MCP on `/mcp` with the OAuth resource server and rate limits (`resource.go`), OIDC sign-in (`login.go`), dev sign-in; both seams ([go/README.md](go/README.md)) | `go test -race ./...` |
| [csharp/](csharp/) | ASP.NET Core minimal APIs, Npgsql `NpgsqlDataSource`, `Microsoft.IdentityModel` | REST under `/api`, MCP on `/mcp` with the OAuth resource server and rate limits (`ResourceServer.cs`), OIDC sign-in (`SignIn.cs`), dev sign-in; both seams ([csharp/README.md](csharp/README.md)) | `dotnet test Gateway.Tests` |
| [rust/](rust/) | axum, tokio-postgres with deadpool-postgres, `jsonwebtoken`, `reqwest` | REST under `/api`, MCP on `/mcp` with the OAuth resource server and rate limits (`resource_server.rs`), OIDC sign-in (`login.rs`), dev sign-in; both seams ([rust/README.md](rust/README.md)) | `cargo test` |

Every example answers `/api/openapi.json`, public and signed-in routes, coded
problems and the development sign-in the same way, and names its connections
`pgmi-gateway-<language>`. All five serve MCP with the OAuth resource server,
OIDC sign-in and the rate limits, held to the same end-to-end tests.

Each language's README has the same sections, in the same order: run it,
how a request flows, calling a new handler, the two seams, OAuth and
sign-in, and what to adapt.

## Adding behaviour: the two seams

Each gateway has two extension points ([PROTOCOL.md](PROTOCOL.md),
"Extension seams"). Every gateway has both; its own README shows how to add a
client call, a decorator and an interceptor.

- **The database channel**: every database call behind one interface.
  Retry, tracing, caching or a test double is a decorator added at startup.
- **Interceptors**: change a request before its transaction opens, or a
  response after its commit. Each selects itself from what the database
  publishes about the handler, such as an `x-embed` annotation on an
  `inputSchema` property.

Embeddings: mark the text property `"x-embed": true` and declare
`<property>_embedding` beside it in the handler's `inputSchema`, then give
the gateway an embedder that calls your model through one shared, pooled
client. Signed storage URLs: a response interceptor that signs exactly
what the handler returned, with the key from configuration.

The tests need an `env=dev` deploy, a login in the api role and
`DATABASE_URL` pointing at it as that login (the project README's Quick
Start, "Run a gateway", walks through it; `GATEWAY_PASSWORD` holds the
password typed at `\password`):

```bash
pgmi deploy . -d mydb --param env=dev
psql -d mydb -c "CREATE ROLE mydb_gateway LOGIN IN ROLE mydb_api"
psql -d mydb -c "\\password mydb_gateway"
export DATABASE_URL="postgresql://mydb_gateway:$GATEWAY_PASSWORD@localhost:5432/mydb"
```

That login can sign anyone in and act as every person who approved an OAuth
client ([PROTOCOL.md, "Who talks to PostgreSQL"](PROTOCOL.md#who-talks-to-postgresql)):
keep `DATABASE_URL` as secret as a token-signing key, give each gateway its
own login, and never share it with jobs or tooling.

Without `DATABASE_URL` the database tests are skipped. The MCP database tests
call `echo_note`, the example tool in `api/examples.sql`; when you replace it,
point them at one of yours.
