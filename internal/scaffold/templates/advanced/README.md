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

**Deployment connection must be a superuser.** `lib/core/entity-standards.sql` installs a DDL event trigger (`core_entity_table_standards`) — `CREATE EVENT TRIGGER` is a superuser-only operation. pgmi fails fast at install time with an actionable error if the connection lacks `rolsuper`. Managed PostgreSQL providers that do not expose superuser access (most AWS RDS tiers, some Azure Flexible Server configurations) cannot run this template as-is. If that applies to you: strip `lib/core/entity-standards.sql` from the deployment and add `created_at`/`deleted_at` columns explicitly on every entity table.

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

    // Build headers hstore for PostgreSQL
    headers := map[string]string{
        "x-user-id": userID,
        "content-type": r.Header.Get("Content-Type"),
    }

    // Call PostgreSQL handler
    var response HTTPResponse
    err := db.QueryRow(ctx,
        "SELECT * FROM api.rest_invoke($1, $2, $3, $4)",
        r.Method, r.URL.Path, headers, body,
    ).Scan(&response.Status, &response.Headers, &response.Body)

    // Gateway responsibility: sanitize errors
    if err != nil {
        log.Error("PostgreSQL error", "error", err) // Log internally
        http.Error(w, "Internal Server Error", 500) // Sanitized response
        return
    }

    w.WriteHeader(response.Status)
    w.Write(response.Body)
}
```

## Project Structure

```
myproject/
├── lib/                      # Pre-built framework — extend around it; see "Trimming the template"
│   ├── api/                  # HTTP framework (types, routing, gateways)
│   ├── core/                 # Managed object infrastructure
│   ├── internal/             # Deployment tracking, text attributes
│   ├── common/               # Cross-cutting primitives (casting, encoding, text)
│   └── __test__/             # Framework tests
├── api/                      # YOUR API HANDLERS
│   └── examples.sql          # Starting point - modify/replace this
├── __test__/                 # YOUR TESTS
├── deploy.sql                # Deployment orchestrator (includes infrastructure bootstrap)
├── pgmi.yaml                 # Project configuration (connection, params, timeout)
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
- `lib/core/entity-standards.sql` — the superuser-only DDL event trigger. Required strip for managed providers without superuser access (most AWS RDS tiers, Cloud SQL, Supabase, Neon). After removing it, declare `created_at`/`deleted_at` explicitly on entity tables.

**Load-bearing — removing these is a rework, not a delete:**
- **Role hierarchy and schema grants** — `database_owner/admin/api/customer` own every object and back every `GRANT` and RLS policy. If your org manages roles externally, override the role *names* via parameters (see Parameters below); don't delete the hierarchy.
- **MCP support** — woven through the shared API files (types, handler registry, helpers, gateways), not isolated to the `*-mcp-*` files. If you don't use MCP, simply don't register MCP handlers — the protocol code stays dormant and harmless. Physically removing it means editing the framework's core files.

## Quick Start

### 1. Deploy

Role passwords are required (see [Parameters](#parameters)). Pass them via a
params file, **never as command-line `--param`** — values on the command line
leak to the process list (`ps`), shell history, and CI logs. Use strong,
generated values; the names below are placeholders.

```bash
# Write the secrets file (add it to .gitignore), deploy, then remove it.
umask 077
cat > secrets.env <<'EOF'
database_admin_password=CHANGE_ME
database_customer_password=CHANGE_ME
EOF

pgmi deploy . --database myapp_dev --params-file secrets.env

rm -f secrets.env
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
        'uri', '^/my-endpoint$',
        'httpMethod', '^GET$',
        'name', 'my_endpoint',
        'description', 'My custom endpoint'
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
    v_response := api.rest_invoke('GET', '/my-endpoint', NULL, NULL::bytea);

    IF (v_response).status_code != 200 THEN
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
| `sortKeys` | Yes | Execution order (lexicographic) |
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
        'uri', '^/users/([0-9]+)$',
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

## Parameters

| Parameter | Default | Required | Description |
|-----------|---------|----------|-------------|
| `database_admin_password` | - | **Yes** | Admin role password |
| `database_customer_password` | - | **Yes** | Customer role password |
| `database_owner_role` | `<dbname>_owner` | No | Owner role (NOLOGIN) |
| `database_admin_role` | `<dbname>_admin` | No | Admin role (LOGIN, full access) |
| `database_api_role` | `<dbname>_api` | No | API group role (NOLOGIN, permission bundle) |
| `database_customer_role` | `<dbname>_customer` | No | Customer role (LOGIN, RLS-restricted) |
| `env` | `development` | No | Environment name |

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

database_customer_role (LOGIN)
  └── inherits: api
  └── RLS-restricted access
```

## Four-Schema Design

| Schema | Purpose | Access |
|--------|---------|--------|
| `common` | Cross-cutting primitives (casting, encoding, text) | All roles |
| `api` | HTTP types, routing, handlers | API role (EXECUTE only) |
| `core` | Business domain (your tables) | Admin role |
| `internal` | Deployment tracking, infrastructure | Owner only |

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

Your files execute after framework files (use sortKeys `005/xxx` or higher).

## Testing

Tests run as part of deployment via the `pgmi_test()` macro in deploy.sql. Each test runs in a savepoint that rolls back, so test data never persists while your migrations commit.

To filter tests, pass a pattern to the macro in deploy.sql:

```sql
-- Run only API tests
CALL pgmi_test('.*/api/.*');
```

## Troubleshooting

### "Required parameter missing"
Provide the required password parameters via a params file (see [Deploy](#1-deploy)) — not on the command line:
```bash
pgmi deploy . -d mydb --params-file secrets.env
```

### Script execution order issues
Check your `<sortKeys>` - lower values execute first.

### Extending framework code
Create files in root directories (api/, common/, core/) not in lib/.
Use sortKeys `005/xxx` or higher to execute after framework.
