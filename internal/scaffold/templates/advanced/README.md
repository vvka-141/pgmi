# Advanced Template

An optional architecture where PostgreSQL is your application server — HTTP routing, business logic, and data in one transactional system. Treat it as a starting point to own and adapt, not a worldview pgmi requires.

> **New to this approach?** Read [ARCHITECTURE.md](ARCHITECTURE.md) first to understand the "Application as Dataset" philosophy, layered schema design, and when this template is right for your project.

## When to Use This Template

**Choose this template if:**
- Business logic belongs in the database (transactional guarantees for all state changes)
- You need multi-protocol support (REST, RPC, MCP)
- Data integrity is critical (financial, healthcare, compliance)
- Your team has solid PostgreSQL skills

**Prerequisites:** Familiarity with PostgreSQL functions, views, and transactions. See [ARCHITECTURE.md](ARCHITECTURE.md) for the full decision guide.

**No superuser required.** The template works with any role that can create roles, schemas, and extensions. Entity lifecycle standards (`created_at`/`deleted_at`) are enforced by a deploy-end sweep over `pg_temp` functions — no DDL event trigger, no superuser privilege. Compatible with managed PostgreSQL providers (AWS RDS, Cloud SQL, Azure Flexible Server, Supabase, Neon).

## Deployment Architecture

**IMPORTANT**: The HTTP handlers in this template are designed to run **behind a web server or API gateway** (Go, Node.js, nginx, Envoy, AWS API Gateway, etc.). PostgreSQL is NOT directly exposed to the internet.

```
┌─────────────────────────────────────────────────────────────────┐
│                        Internet                                  │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Web Server / API Gateway                      │
│  ─────────────────────────────────────────────────────────────  │
│  • TLS termination                                               │
│  • Rate limiting & DoS protection                                │
│  • Request validation (headers, size limits)                     │
│  • Authentication (JWT/OAuth token validation)                   │
│  • CORS headers                                                  │
│  • Error sanitization (don't leak internal errors)               │
│  • Request/response logging                                      │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ Calls PostgreSQL functions:
                              │   api.rest_invoke(method, url, headers, content)
                              │   api.rpc_invoke(route_id, headers, content)
                              │   api.mcp_call_tool(name, args, context, request_id)
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                         PostgreSQL                               │
│  ─────────────────────────────────────────────────────────────  │
│  • Transactional request handling (all-or-nothing)               │
│  • Business logic execution                                      │
│  • Row-Level Security (RLS) enforcement                          │
│  • Single-roundtrip complex operations                           │
│  • Data validation via constraints                               │
│  • Audit logging                                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Why This Architecture?

| Concern | Handled By | Rationale |
|---------|------------|-----------|
| TLS/HTTPS | Gateway | Web servers excel at TLS, connection pooling |
| Rate limiting | Gateway | Stateless, before hitting database |
| Header validation | Gateway | Reject malformed requests early |
| Token validation | Gateway | JWT/OAuth libraries in web server |
| CORS | Gateway | Simple header manipulation |
| Error sanitization | Gateway | Don't leak SQLERRM to clients |
| Business logic | PostgreSQL | Transactional, close to data |
| Authorization (RLS) | PostgreSQL | Row-level filtering at query time |
| Data validation | PostgreSQL | Constraints, triggers |
| Complex operations | PostgreSQL | Single roundtrip, ACID guarantees |

### Example Gateway Integration (Go)

```go
func handleREST(w http.ResponseWriter, r *http.Request) {
    // Gateway responsibilities: validate token, set identity
    userID := validateJWT(r.Header.Get("Authorization"))
    if userID == "" {
        http.Error(w, "Unauthorized", 401)
        return
    }

    // Forward every request header; identity comes from the gateway, never
    // from the client, so drop any x-user-id the client sent before setting it.
    headers := map[string]string{}
    for name, values := range r.Header {
        headers[strings.ToLower(name)] = strings.Join(values, ", ")
    }
    headers["x-user-id"] = userID

    // RequestURI keeps the query string; URL.Path would drop it.
    var response HTTPResponse
    err := db.QueryRow(ctx,
        "SELECT * FROM api.rest_invoke($1, $2, $3, $4)",
        r.Method, r.URL.RequestURI(), headers, body,
    ).Scan(&response.Status, &response.Headers, &response.Body)

    // Gateway responsibility: sanitize errors
    if err != nil {
        log.Error("PostgreSQL error", "error", err) // Log internally
        http.Error(w, "Internal Server Error", 500) // Sanitized response
        return
    }

    // Copy every response header: Allow on 405, WWW-Authenticate on 401,
    // Content-Type, Cache-Control, ETag and Vary all come from PostgreSQL.
    for name, value := range response.Headers {
        w.Header().Set(name, value)
    }
    w.WriteHeader(response.Status)
    w.Write(response.Body)
}
```

## Project Structure

```
myproject/
├── lib/                      # Pre-built framework — extend around it; see "Trimming the template"
│   ├── api/                  # HTTP framework (types, routing, gateways, MCP)
│   ├── core/                 # Managed object infrastructure
│   ├── common/               # Cross-cutting primitives (casting, encoding, text)
│   ├── __test__/             # Framework tests
│   └── README.md             # Framework API reference (also `pgmi ai skill advanced-template`)
├── membership/               # Identity, organizations, invitations, RLS, API keys
├── api/                      # YOUR API HANDLERS
│   └── examples.sql          # Starting point - modify/replace this
├── __test__/                 # YOUR TESTS
├── tools/                    # MCP HTTP gateway (mcp-gateway.py, requirements.txt)
├── deploy.sql                # Deployment orchestrator (includes infrastructure bootstrap)
├── session.xml               # Parameter declarations consumed by deploy.sql
├── pgmi.yaml                 # Project configuration (connection, params, timeout)
├── ARCHITECTURE.md           # Design rationale
└── README.md
```

### What Goes Where

| Directory | Purpose | Modify? |
|-----------|---------|---------|
| `lib/` | Framework code (HTTP routing, types, utilities) | Rarely - extend in root dirs instead |
| `api/` | Your HTTP handlers (REST, RPC, MCP) | Yes - your application code |
| `__test__/` | Your application tests | Yes - add tests here |
| `deploy.sql` | Deployment phases, transaction control, infrastructure bootstrap | Yes - customize deployment |

## Trimming the template

This template is a working reference system, not a framework you must keep whole. The `lib/` files are coupled and tested — extend *around* them in root directories rather than editing internals — but you are meant to own the result and cut what you don't need.

**Safe to delete:**
- `api/examples.sql` — placeholder REST/RPC/MCP handlers. Replace with your own.
- `lib/core/entity-standards.sql` — the deploy-end entity standards sweep. Without it, declare `created_at`/`deleted_at` explicitly on entity tables.

**Load-bearing — removing these is a rework, not a delete:**
- **Role hierarchy and schema grants** — `database_owner/admin/api/customer` own every object and back every `GRANT` and RLS policy. If your org manages roles externally, override the role *names* via parameters (see Parameters below); don't delete the hierarchy.
- **MCP support** — woven through the shared API files (types, handler registry, helpers, gateways), not isolated to the `*-mcp-*` files. If you don't use MCP, simply don't register MCP handlers — the protocol code stays dormant and harmless. Physically removing it means editing the framework's core files.

## Quick Start

### 1. Deploy

Every deploy says which environment it targets with `env`. There is no default.

On a local, disposable database, `env=dev` is enough: missing role passwords
default to `postgres`, with a warning.

```bash
pgmi deploy . --database myapp_dev --param env=dev
```

Anywhere else, set `env` to something other than `dev` and pass the admin
password in a params file, **never as command-line `--param`**. Values on the
command line leak to the process list (`ps`), shell history, and CI logs. Use
strong, generated values; the names below are placeholders.

```bash
# Write the params file (*.params is gitignored), deploy, then remove it.
umask 077
cat > prod.params <<'EOF'
env=prod
database_admin_password=CHANGE_ME
EOF

pgmi deploy . --database myapp --params-file prod.params

rm -f prod.params
```

In CI, generate `secrets.env` from your pipeline's secret store. See the
[pgmi Security Guide](https://github.com/vvka-141/pgmi/blob/main/docs/SECURITY.md)
for the full CI pattern.

### 2. Test the Examples

Tests run automatically as part of deployment via the `pgmi_test()` macro in deploy.sql. If all tests pass, the deployment commits. If any test fails, the deployment rolls back.

### 3. Add Your Own Handler

Edit `api/examples.sql` or create a new file in `api/`:

```sql
/*
<pgmi-meta
    id="YOUR-UUID-HERE"
    idempotent="true">
  <description>My custom handler</description>
  <sortKeys>
    <key>005/002</key>
  </sortKeys>
</pgmi-meta>
*/

SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'YOUR-HANDLER-UUID',
        'path', '/my-endpoint',
        'httpMethod', '^GET$',
        'name', 'my_endpoint',
        'description', 'My custom endpoint',
        'requiresAuth', false,
        'outputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object('message', jsonb_build_object('type', 'string')),
            'required', jsonb_build_array('message')
        )
    ),
    $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object(
        'message', 'Hello from my endpoint!'
    ));
END;
    $body$
);
```

### 4. Add Tests

Create `__test__/my_tests.sql`:

```sql
/*
<pgmi-meta id="YOUR-TEST-UUID" idempotent="true">
  <description>My endpoint tests</description>
</pgmi-meta>
*/

DO $$
DECLARE
    v_response api.http_response;
BEGIN
    v_response := api.rest_invoke('GET', '/my-endpoint', ''::extensions.hstore, NULL::bytea);

    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'Expected 200, got %', (v_response).status_code;
    END IF;

    RAISE NOTICE '✓ my_endpoint returns 200';
END $$;
```

## Metadata-Driven Deployment

The advanced template uses pgmi's optional metadata system for path-independent tracking, idempotency control, and explicit ordering. For a complete guide, see [docs/METADATA.md](https://github.com/vvka-141/pgmi/blob/main/docs/METADATA.md).

Every script requires a `<pgmi-meta>` block:

```sql
/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>What this script does</description>
  <sortKeys>
    <key>005/001</key>
  </sortKeys>
</pgmi-meta>
*/
```

### Attributes

| Attribute | Required | Description |
|-----------|----------|-------------|
| `id` | Yes | UUID for path-independent tracking |
| `idempotent` | Yes | `true` = re-run every deploy, `false` = once only |

### Child elements

| Element | Required | Description |
|---------|----------|-------------|
| `sortKeys` | No | Execution order (lexicographic). Falls back to file path when absent |
| `description` | No | Human-readable purpose |

### Sort Key Conventions

| Range | Purpose |
|-------|---------|
| `001/xxx` | Utils (no dependencies) |
| `002/xxx` | Internal infrastructure |
| `003/xxx` | Core domain |
| `004/xxx` | API framework |
| `005/xxx` | User application code |

## Three Protocols

The framework supports REST, RPC, and MCP protocols:

### REST Handlers

```sql
SELECT api.create_or_replace_rest_handler(
    jsonb_build_object(
        'id', 'handler-uuid',
        'path', '/users/{userId}',
        'httpMethod', '^GET$',
        'name', 'get_user',
        'description', 'Get user by ID'
    ),
    $body$
DECLARE
    v_user_id int;
BEGIN
    v_user_id := (regexp_matches((request).url, '/users/([0-9]+)'))[1]::int;
    RETURN api.json_response(200, (SELECT row_to_json(u) FROM users u WHERE id = v_user_id));
END;
    $body$
);

-- Invoke: api.rest_invoke('GET', '/users/123', headers, content)
```

### RPC Handlers

```sql
SELECT api.create_or_replace_rpc_handler(
    jsonb_build_object(
        'id', 'handler-uuid',
        'methodName', 'users.create',
        'description', 'Create a new user'
    ),
    $body$
BEGIN
    -- JSON-RPC 2.0 handler
    RETURN api.jsonrpc_success(
        jsonb_build_object('userId', 123),
        api.content_json((request).content)->'id'
    );
END;
    $body$
);

-- Invoke: api.rpc_invoke(api.rpc_resolve('users.create'), headers, content)
```

### MCP Handlers

```sql
SELECT api.create_or_replace_mcp_handler(
    jsonb_build_object(
        'id', 'handler-uuid',
        'type', 'tool',
        'name', 'query_database',
        'description', 'Execute a read-only query',
        'inputSchema', jsonb_build_object(
            'type', 'object',
            'properties', jsonb_build_object(
                'sql', jsonb_build_object('type', 'string')
            ),
            'required', jsonb_build_array('sql')
        )
    ),
    $body$
BEGIN
    RETURN api.mcp_tool_result(
        jsonb_build_array(api.mcp_text('Query result: ...')),
        (request).request_id,
        false
    );
END;
    $body$
);

-- Invoke: api.mcp_call_tool('query_database', arguments, context, request_id)
```

Any handler may declare a minimum isolation floor (`minTransactionIsolation`) and a read-only policy (`readOnly`). The client gateway resolves the policy before `BEGIN`; the database enforces it fail-closed. See [Transaction policy](https://vvka-141.github.io/pgmi/docs/advanced/transaction-policy/).

## Parameters

| Parameter | Default | Required | Description |
|-----------|---------|----------|-------------|
| `env` | - | **Yes** | Environment name. `dev` defaults missing passwords to `postgres` |
| `database_admin_password` | `postgres` when `env=dev` | **Yes**, unless `env=dev` | Admin role password |
| `database_owner_role` | `<dbname>_owner` | No | Owner role (NOLOGIN) |
| `database_admin_role` | `<dbname>_admin` | No | Admin role (LOGIN, full access) |
| `database_api_role` | `<dbname>_api` | No | API group role (NOLOGIN, permission bundle) |
| `database_customer_role` | `<dbname>_customer` | No | Customer role (NOLOGIN, RLS-restricted) |

Pass the required password parameters via `--params-file` (or a CI/CD-generated
seeding file), never as command-line `--param` — see [Deploy](#1-deploy).

## Role Hierarchy

```
database_owner_role (NOLOGIN)
  └── owns all database objects

database_api_role (NOLOGIN)
  └── permission bundle for API access

database_admin_role (LOGIN)
  └── inherits: owner + api
  └── full database access

database_customer_role (NOLOGIN)
  └── inherits: nothing (holds direct, RLS-scoped grants)
  └── RLS-restricted access
```

The customer role cannot log in. Row-level security identifies the caller by
`auth.idp_subject`, a session setting any session can set, so only the gateway
(which sets it from the authenticated request) may act for a user. Grant the
customer role to your own login role only for a connection you trust to set
that identity.

## Schema Design

`deploy.sql` creates six schemas. `search_path` is set database-wide to
`core, api, membership, internal, extensions, common, pg_temp`.

| Schema | Purpose | USAGE granted to |
|--------|---------|------------------|
| `common` | Cross-cutting primitives (casting, encoding, text) | admin, api, customer |
| `api` | HTTP types, routing, handlers | api, customer |
| `core` | Business domain (your tables) | api |
| `membership` | User identity, organizations, invitations, access control | admin, api, customer |
| `internal` | Deployment tracking, infrastructure | owner only |
| `extensions` | `uuid-ossp` and `pgcrypto`, kept out of `search_path` conflicts | PUBLIC |

## Extending the Framework

To add custom utilities or types, create files in root directories (not `lib/`):

```
myproject/
├── lib/                      # Framework (don't modify)
├── api/
│   ├── examples.sql          # Framework examples
│   └── my_handlers.sql       # YOUR handlers
├── common/                   # YOUR cross-cutting helpers (create if needed)
│   └── my_helpers.sql
└── core/                     # YOUR domain (create if needed)
    └── my_tables.sql
```

Every SQL file needs a `<pgmi-meta>` block giving its place in the plan, with a
sort key of `005/...` or later so it runs after the framework:

```sql
/*
<pgmi-meta id="<a new uuid>" idempotent="true">
  <sortKeys><key>005/010</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE IF NOT EXISTS core.my_table (...);
```

A file without one would sort by its path, ahead of the framework, so
`deploy.sql` stops before running anything and names it.

## Working with an AI assistant

This template has real conventions an assistant will not guess — the four-phase
handler body, kernel-before-handler sort keys, `<pgmi-meta>` blocks, mandatory
`outputSchema`, RLS through `api.current_member_org_ids()`. Give it the rules
before it writes any SQL:

```bash
pgmi ai setup          # writes .claude/skills/pgmi/ — commit it
pgmi ai check          # is that guidance current for this binary?
```

`--assistant cursor|copilot|windsurf|cline` targets other tools. To read the
same material yourself: `pgmi ai`, `pgmi ai skills`, `pgmi ai skill
pgmi-endpoint-quickstart`.

## Testing

Tests run as part of deployment via the `pgmi_test()` macro in deploy.sql. Each
test runs in a savepoint that rolls back its transactional changes while your
migrations commit. Sequence advances and external effects are not rolled back.

To filter tests, pass a pattern to the macro in deploy.sql:

```sql
-- Run only API tests
CALL pgmi_test('.*/api/.*');
```

## Troubleshooting

### "Missing required parameters"
Every deploy needs `env`, and every deploy with an `env` other than `dev` needs
`database_admin_password`. Pass them in a params file (see [Deploy](#1-deploy)),
not on the command line:
```bash
pgmi deploy . -d mydb --params-file prod.params
```

### "canceling statement due to lock timeout" (55P03)
`deploy.sql` sets `lock_timeout = '5s'`: a deploy that needs a table lock held
by a long-running application transaction fails instead of queueing every
query behind it. Retry when the load drops. An unchanged redeploy takes no
ACCESS EXCLUSIVE lock on tables: RLS goes through `core.ensure_rls` /
`core.ensure_policy`, which run DDL only when the policy changed, and
evolution-path `ALTER TABLE`s check `pg_temp.has_column` first, and views go
through `core.ensure_view`, which replaces a view only when its statement
changed. `CREATE OR REPLACE VIEW` locks the view exclusively even when the
definition is identical, so wrap your own views the same way.

### Script execution order issues
Check your `<sortKeys>` - lower values execute first.

### Extending framework code
Create files in root directories (api/, common/, core/) not in lib/.
Use sortKeys `005/xxx` or higher to execute after framework.
