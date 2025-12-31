# Advanced Template

PostgreSQL as your application server - HTTP routing, business logic, and data in one transactional system.

> **New to this approach?** Read [ARCHITECTURE.md](ARCHITECTURE.md) first to understand the "Application as Dataset" philosophy, layered schema design, and when this template is right for your project.

## When to Use This Template

**Choose this template if:**
- Business logic belongs in the database (transactional guarantees for all state changes)
- You need multi-protocol support (REST, RPC, MCP)
- Data integrity is critical (financial, healthcare, compliance)
- Your team has solid PostgreSQL skills

**Prerequisites:** Familiarity with PostgreSQL functions, views, and transactions. See [ARCHITECTURE.md](ARCHITECTURE.md) for the full decision guide.

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
├── lib/                      # FRAMEWORK (don't modify unless extending)
│   ├── api/                  # HTTP framework (types, routing, gateways)
│   ├── core/                 # Managed object infrastructure
│   ├── internal/             # Deployment tracking, text attributes
│   ├── utils/                # Type casting, text utilities
│   └── __test__/             # Framework tests
├── api/                      # YOUR API HANDLERS
│   └── examples.sql          # Starting point - modify/replace this
├── __test__/                 # YOUR TESTS
├── deploy.sql                # Deployment orchestrator
├── init.sql                  # Infrastructure bootstrap (roles, schemas)
└── README.md
```

### What Goes Where

| Directory | Purpose | Modify? |
|-----------|---------|---------|
| `lib/` | Framework code (HTTP routing, types, utilities) | Rarely - extend in root dirs instead |
| `api/` | Your HTTP handlers (REST, RPC, MCP) | Yes - your application code |
| `__test__/` | Your application tests | Yes - add tests here |
| `deploy.sql` | Deployment phases and transaction control | Yes - customize deployment |
| `init.sql` | Roles, schemas, extensions | Yes - customize infrastructure |

## Quick Start

### 1. Deploy

```bash
pgmi deploy . --database myapp_dev \
  --param database_admin_password="AdminPass123!" \
  --param database_api_password="ApiPass123!"
```

### 2. Test the Examples

```bash
# Run all tests
pgmi test . -d myapp_dev
```

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
    v_response := api.rest_invoke('GET', '/my-endpoint', NULL, NULL);

    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'Expected 200, got %', (v_response).status_code;
    END IF;

    RAISE NOTICE '✓ my_endpoint returns 200';
END $$;
```

## Metadata-Driven Deployment

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
| `database_api_password` | - | **Yes** | API role password |
| `database_owner_role` | `<dbname>_owner` | No | Owner role (NOLOGIN) |
| `database_admin_role` | `<dbname>_admin` | No | Admin role (LOGIN) |
| `database_api_role` | `<dbname>_api` | No | API role (LOGIN) |
| `env` | `development` | No | Environment name |

## Role Hierarchy

```
database_owner_role (NOLOGIN)
    ↑ INHERIT TRUE
database_admin_role (LOGIN) ← For administrators

database_api_role (LOGIN) ← For API clients (EXECUTE only)
```

## Four-Schema Design

| Schema | Purpose | Access |
|--------|---------|--------|
| `utils` | Type casting, text utilities | All roles |
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
├── utils/                    # YOUR utilities (create if needed)
│   └── my_utils.sql
└── core/                     # YOUR domain (create if needed)
    └── my_tables.sql
```

Your files execute after framework files (use sortKeys `005/xxx` or higher).

## Testing

Tests run in a transaction that automatically rolls back:

```bash
# Run all tests
pgmi test . -d myapp_dev

# Run filtered tests
pgmi test . -d myapp_dev --filter "/api/"
```

## Troubleshooting

### "Required parameter missing"
```bash
pgmi deploy . -d mydb \
  --param database_admin_password="..." \
  --param database_api_password="..."
```

### Script execution order issues
Check your `<sortKeys>` - lower values execute first.

### Extending framework code
Create files in root directories (api/, utils/, core/) not in lib/.
Use sortKeys `005/xxx` or higher to execute after framework.
