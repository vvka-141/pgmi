---
name: pgmi-templates
description: "Use when creating or modifying scaffold templates"
user_invocable: true
---


**Use this skill when:**
- Creating or modifying scaffolding templates
- Understanding metadata-driven deployment
- Debugging scaffolding or template issues
- Designing custom project structures
- Working with inline `<pgmi-meta>` blocks in SQL files

---

## Template System Overview

pgmi's scaffolding system provides project templates that demonstrate different deployment patterns. Templates are embedded in the CLI binary and initialized via `pgmi init <project> --template <name>`.

**Architecture:**
- **Storage:** Embedded filesystem (`internal/scaffold/templates/`)
- **Discovery:** Automatic (reads `templates/` directory)
- **Rendering:** Simple variable substitution (`{{PROJECT_NAME}}`)
- **CLI:** `pgmi init`, `pgmi templates list`, `pgmi templates describe`

**Key Files:**
- `internal/scaffold/scaffold.go` - Main scaffolder
- `internal/cli/init.go` - CLI command
- `internal/cli/templates.go` - Template discovery

---

## Available Templates

### Template Comparison

| Aspect | **basic** | **advanced** |
|--------|-----------|--------------|
| **Purpose** | Learning pgmi fundamentals | Production-ready deployments |
| **Structure** | Flat with `migrations/` folder | Domain-organized (common/api/core/internal) |
| **Execution Model** | Directory-based phases | Metadata-driven UNNEST sort key ordering |
| **Ordering** | Alphabetical (lexicographic) | Explicit sort keys (lexicographic on keys) |
| **Tracking** | None (stateless) | UUID-based in `internal.deployment_script_execution_log` |
| **Metadata** | Optional/absent | Required `<pgmi-meta>` blocks |
| **Dependencies** | Implicit (directory structure) | Implicit via sort key layering |
| **Schemas** | `public` (default) | Four-schema (common/api/core/internal) |
| **Role Hierarchy** | None | Three-tier (owner/admin/api) |
| **HTTP Framework** | No | Yes (routing, handlers, queuing) |
| **Idempotency** | Manual (user-written) | Metadata-driven (`idempotent` flag) |
| **Lines of Code** | ~100 | ~1500 |
| **Best For** | Learning, simple migrations | Production, complex apps |

### basic Template

**Location:** `internal/scaffold/templates/basic/`

**Structure:**
```
basic/
├── deploy.sql              # Main orchestrator
├── migrations/
│   ├── 001_users.sql       # Users table
│   └── 002_user_crud.sql   # CRUD functions
├── __test__/
│   ├── _setup.sql          # Test fixture (creates test users)
│   └── test_user_crud.sql  # CRUD function tests
├── pgmi.yaml               # Project configuration
└── README.md               # User guide
```

**Key Features:**
- Single-transaction deployment (`BEGIN;...COMMIT;`)
- Savepoint-based test isolation
- Directory-based phase ordering
- Fixture-based test setup (`_setup.sql`)

**Parameter Contract:**
- No required parameters (self-contained example)

**Use Cases:**
- Learning pgmi deployment flow
- Simple migration scripts
- Prototyping deployment logic
- Teaching pgmi to teams

### advanced Template

**Location:** `internal/scaffold/templates/advanced/`

**Structure:**
```
advanced/
├── deploy.sql                          # Metadata-driven orchestrator
├── session.xml                         # Session metadata
├── pgmi.yaml                           # Project configuration
├── README.md                           # Comprehensive guide
├── ARCHITECTURE.md                     # Architecture documentation
├── api/
│   ├── examples.sql                    # Example API handlers
│   └── __test__/
│       ├── _setup.sql                  # API test fixture
│       └── test_authenticated_api.sql  # Authentication tests
├── lib/                                # Reusable library code
│   ├── README.md                       # Library documentation
│   ├── api/                            # Multi-protocol API framework
│   │   ├── 01-types.sql                # Core types
│   │   ├── 02-handler-registry.sql     # Handler metadata storage
│   │   ├── 03-rest-routes.sql          # REST route definitions
│   │   ├── 04-rpc-routes.sql           # JSON-RPC route definitions
│   │   ├── 05-mcp-routes.sql           # MCP protocol routes
│   │   ├── 06-queue-infrastructure.sql # Async task queue
│   │   ├── 07-helpers.sql              # Response builders, error helpers
│   │   ├── 08-registration.sql         # Handler registration functions
│   │   ├── 09-gateways.sql             # Protocol gateway functions
│   │   ├── 10-mcp-protocol.sql         # MCP protocol handler
│   │   └── views.sql                   # API views
│   ├── core/
│   │   ├── foundation.sql              # Domain schema setup
│   │   └── attached-properties.sql     # Attached property utilities
│   ├── common/
│   │   ├── cast.sql                    # Safe type casting (`?|` operator, try_cast overloads)
│   │   ├── encoding.sql                # Bytea encoding domains (utf8/latin1/win1252) and converters
│   │   └── text.sql                    # Text utilities with inline tests
│   └── __test__/                       # Library tests
│       ├── test_api_protocols.sql      # REST/RPC/MCP protocol tests
│       ├── test_auth_enforcement.sql   # Authentication tests
│       ├── test_error_handling.sql     # Error mapping tests
│       ├── test_handler_lifecycle.sql  # Handler registration tests
│       ├── test_mcp_protocol.sql       # MCP protocol tests
│       └── test_migrations_tracking.sql # Deployment tracking tests
├── membership/                         # Membership domain
│   ├── 01-schema.sql                   # Membership schema
│   ├── 02-views.sql                    # Membership views
│   ├── 03-functions.sql                # Membership functions
│   ├── 04-claims.sql                   # Claims handling
│   ├── 05-current-user.sql             # Current user context
│   ├── 06-rls.sql                      # Row-level security
│   └── __test__/
│       ├── _setup.sql                  # Membership test fixture
│       ├── test_account_linking.sql    # Account linking tests
│       ├── test_default_org.sql        # Default org tests
│       ├── test_invite_flow.sql        # Invite flow tests
│       └── test_user_upsert.sql        # User upsert tests
├── tools/
│   ├── mcp-gateway.py                  # MCP gateway tool
│   └── requirements.txt                # Python dependencies
└── __test__/
    └── README.md                       # Test documentation
```

**Key Features:**
- Metadata-driven execution (UNNEST sort key ordering)
- UUID-based script tracking (survives renames)
- Four-schema architecture (internal, core, api, public)
- Three-tier role hierarchy (owner/admin/api)
- Multi-protocol API framework (REST, JSON-RPC, MCP)
- Authentication enforcement via handler metadata
- Inline testing patterns (in lib/common/)
- Comprehensive deployment history
- Optional test plan persistence via `pgmi_persist_test_plan()`

**Parameter Contract:**
- `database_admin_password` (REQUIRED) - Password for the admin LOGIN role
- `database_customer_password` (REQUIRED) - Password for the customer LOGIN role
- `env` (optional) - Environment identifier (dev/staging/prod)

The API role is a NOLOGIN group role (a permission bundle), so it has no password.

**Use Cases:**
- Production database deployments
- Multi-schema applications
- API-first database design
- Complex multi-phase execution ordering
- Auditable deployment history

---

## Creating Custom Templates

### Step 1: Directory Structure

Create your template under `internal/scaffold/templates/<name>/`:

```bash
internal/scaffold/templates/my-template/
├── deploy.sql          # REQUIRED: Main orchestrator
├── README.md           # RECOMMENDED: User guide
├── <your-folders>/     # Your SQL organization
└── __test__/           # Tests (auto-isolated by pgmi)
```

**Required Files:**
- `deploy.sql` - Must exist. Orchestrates deployment by populating `pg_temp.pgmi_plan_view`.

**Recommended Files:**
- `README.md` - User guide explaining template purpose, parameter contracts, usage examples.

**Conventions:**
- `__test__/` or `__tests__/` - Test files automatically isolated from deployment
- Folders organize by domain, phase, or schema (your choice)
- SQL files use descriptive names (`001_create_schema.sql`, not `s1.sql`)

### Step 2: Template Variables

**Currently Supported:**
- `{{PROJECT_NAME}}` - Replaced with user's project name during `pgmi init`

**Example Usage:**
```sql
-- In your SQL files
COMMENT ON DATABASE {{PROJECT_NAME}} IS 'My application database';

-- After pgmi init myapp:
COMMENT ON DATABASE myapp IS 'My application database';
```

**Implementation:** `internal/scaffold/scaffold.go:processTemplate()` (lines 184-189)

**Escaping:** Not currently needed (simple string replacement). Future: May add escaping for special chars.

### Step 3: Parameter Contracts

**Define Required Parameters in README.md:**

```markdown
## Required Parameters

This template requires the following parameters:

- `database_owner` - Database owner role name
- `admin_password` - Password for admin role (secure!)

## Example Usage

```bash
# Non-secret params on the command line; secrets via a params file
# (admin_password etc. — never as command-line --param; argv leaks to ps/CI logs).
pgmi deploy . \
  --param database_owner=myapp_owner \
  --params-file secrets.env
```
```

**Parameter Validation:**
- Currently: No automatic validation (user gets runtime SQL errors if missing)
- Roadmap: Template-level parameter contracts with CLI validation

**Best Practice:**
- Use `COALESCE(current_setting('pgmi.key', true), 'default')` for optional params
- Use `current_setting('pgmi.key')` for required params (fails if missing)
- Templates can define their own helper functions for parameter access (e.g., `deployment_setting()` in the advanced template)

### Step 4: Write deploy.sql

**Minimal Example:**
```sql
-- deploy.sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

**Production Example (Metadata-Driven):**
See `internal/scaffold/templates/advanced/deploy.sql` for complete metadata-driven orchestration with:
- Sort key ordering via UNNEST
- Idempotency tracking
- Execution order calculation
- Multi-phase execution support

### Step 5: Test Your Template

**CRITICAL: Always use EmbedFileSystem for template testing — NEVER real filesystem.**

Template integration tests MUST use the in-memory `EmbedFileSystem` path. This means:
- Read templates from Go's `embed.FS` via `EmbedFileSystem`
- Deploy directly to a real PostgreSQL database using `NewTestDeployerWithFS()`
- No `TempDir()`, no `os.WriteFile()`, no CLI subprocess, no disk I/O

**Why:**
- Fast: no filesystem overhead
- Clean: no temp files to leak or clean up
- Secure: no filesystem permission complications
- Deterministic: embedded content is immutable at compile time

**Unit Tests** (structure validation, no database):
```go
// internal/scaffold/templates_unit_test.go
efs := filesystem.NewEmbedFileSystem(templatesFS, "templates/my-template")
fileScanner := scanner.NewScannerWithFS(checksum.New(), efs)
result, err := fileScanner.ScanDirectory(".")
// Verify: file count, checksums, SQL validity, metadata parsing
```

**Integration Tests** (deploy to real PostgreSQL):
```go
// internal/scaffold/integration_test.go
embedFS := scaffold.GetTemplatesFS()
efs := filesystem.NewEmbedFileSystem(embedFS, "templates/my-template")
deployer := testhelpers.NewTestDeployerWithFS(t, efs)
err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
    ConnectionString: connString,
    DatabaseName:     "pgmi_test_my_template",
    SourcePath:       ".", // EmbedFileSystem root = template root
    Overwrite:        true,
    Force:            true,
    Parameters:       params,
})
```

**NEVER do this for template testing:**
```go
// ❌ WRONG: Real filesystem, CLI subprocess
scaffolder.CreateProject("test_project", "my-template", tmpDir)
cmd := exec.Command("pgmi", "deploy", tmpDir, ...)
```

**Validation Checklist:**
- [ ] deploy.sql populates `pg_temp.pgmi_plan_view` successfully
- [ ] Deployment completes without errors
- [ ] Idempotent redeployment succeeds
- [ ] Test files execute correctly (if applicable)
- [ ] No filesystem I/O in test code (EmbedFileSystem only)

### Step 6: Document Your Template

**Update `internal/cli/templates.go:getTemplateDescriptions()`:**

```go
func getTemplateDescriptions() map[string]TemplateDescription {
    return map[string]TemplateDescription{
        // ... existing templates ...
        "my-template": {
            Name:        "my-template",
            Description: "Brief one-line description",
            BestFor:     "Specific use cases this template serves",
            Features: []string{
                "Key feature 1",
                "Key feature 2",
                "Key feature 3",
            },
        },
    }
}
```

**This enables:**
- `pgmi templates list` - Shows your template
- `pgmi templates describe my-template` - Shows detailed description

---

## Metadata Format Specification (Advanced Template)

The advanced template uses XML metadata blocks to declare script identity and execution characteristics.

### Metadata Block Structure

```sql
/*
<pgmi-meta
    id="UUID"
    idempotent="true|false">
  <description>Human-readable purpose of this file</description>
  <sortKeys>
    <key>layer/sequence-number</key>
    <key>another-layer/sequence-number</key>
  </sortKeys>
</pgmi-meta>
*/

-- Your SQL code here
```

### Field Reference

#### `id` (REQUIRED)
- **Type:** UUID (lowercase, canonical format)
- **Purpose:** Unique identifier for this script
- **Characteristics:**
  - Survives file rename/move operations
  - Used in `internal.deployment_script_execution_log`
  - Used in `internal.deployment_script_execution_log` for tracking
- **Generation:**
  ```bash
  # Linux/Mac
  uuidgen | tr '[:upper:]' '[:lower:]'

  # PowerShell (Windows)
  [guid]::NewGuid().ToString().ToLower()

  # Online
  https://www.uuidgenerator.net/version4
  ```
- **Example:** `id="a1b2c3d4-e5f6-7890-abcd-ef1234567890"`

#### `idempotent` (REQUIRED)
- **Type:** Boolean (`true` or `false`)
- **Purpose:** Declares whether script can be safely re-executed
- **Semantics:**
  - `true` = Functions, views, utility code (safe to re-run with `CREATE OR REPLACE`)
  - `false` = Tables, migrations, structural changes (run once only)
- **Behavior:**
  - `idempotent="true"`: Re-executes if content changes (checksum differs)
  - `idempotent="false"`: Executes only once (tracked by UUID)
- **Example:** `idempotent="true"`

#### `<description>` (RECOMMENDED)
- **Type:** Text
- **Purpose:** Human-readable explanation of script purpose
- **Usage:** Documentation, debugging, audit logs
- **Example:**
  ```xml
  <description>Creates utility functions for type casting with error handling</description>
  ```

#### `<sortKeys>` (OPTIONAL)
- **Type:** List of `<key>` elements
- **Purpose:** Control execution order via lexicographic sort key ordering
- **Format:** Any string convention (e.g., `<layer>/<sequence>`)
- **Behavior:**
  - Each key generates a separate execution entry in `pgmi_plan_view`
  - Multiple keys = multi-phase execution (file runs multiple times)
  - Order: `sort_key ASC, path ASC` (deterministic)
- **Example:**
  ```xml
  <sortKeys>
    <key>10-common/0010</key>
    <key>50-seed/0020</key>
  </sortKeys>
  ```
- **Use Cases:**
  - Ensure foundation scripts run before domain scripts
  - Order within schema (common → internal → core → api)
  - Multi-phase execution (create schema early, seed data later)

#### `<membership>` (XSD-DEFINED, NOT PARSED)
- Defined in `internal/metadata/schema.xsd` but NOT parsed by Go's `Metadata` struct
- XSD validates the XML, but the data is silently ignored
- Do not rely on this element in deploy.sql

#### `<dependency>` (XSD-DEFINED, NOT PARSED)
- Defined in `internal/metadata/schema.xsd` but NOT parsed by Go's `Metadata` struct
- There is no dependency graph or topological sort — execution order is purely sort key based
- Do not rely on this element in deploy.sql

### Complete Example

```sql
/*
<pgmi-meta
    id="e8f9a0b1-c2d3-4e5f-6789-0abcdef12345"
    idempotent="true">
  <description>Text utility functions with inline tests</description>
  <sortKeys>
    <key>00000000-0000-0000-0000-000000000000/0002</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION common.slugify(input_text TEXT)
RETURNS TEXT AS $$
BEGIN
    RETURN lower(regexp_replace(input_text, '[^a-zA-Z0-9]+', '-', 'g'));
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- Inline test (non-transactional, runs immediately)
DO $$
BEGIN
    IF common.slugify('Hello World!') != 'hello-world-' THEN
        RAISE EXCEPTION 'Slugify test failed';
    END IF;
END $$;
```

---

## Execution Order Algorithm

The execution order is determined by `pgmi_plan_view` using simple UNNEST and lexicographic ordering.

### How It Works

**Input:** Source files with optional `<pgmi-meta>` sort keys
**Output:** Linear execution order via `pgmi_plan_view`

**Mechanism:**

```sql
-- pgmi_plan_view joins sources with metadata and UNNESTs sort keys
SELECT
    s.path, s.content,
    md5(s.path::bytea)::uuid AS generic_id,
    m.id,
    COALESCE(m.idempotent, true) AS idempotent,
    unnested.sort_key,
    ROW_NUMBER() OVER (ORDER BY unnested.sort_key, s.path) AS execution_order
FROM pg_temp._pgmi_source s
LEFT JOIN pg_temp._pgmi_source_metadata m ON s.path = m.path
CROSS JOIN LATERAL UNNEST(
    COALESCE(NULLIF(m.sort_keys, '{}'), ARRAY[s.path])
) AS unnested(sort_key)
ORDER BY unnested.sort_key, s.path;
```

**Key behaviors:**
- Files with metadata: each sort key generates a separate execution entry
- Files without metadata: file path is used as sort key (lexicographic order)
- Multi-phase: a file with N sort keys appears N times in the plan
- Deterministic: `ORDER BY sort_key, path` with `ROW_NUMBER()` for tie-breaking

### Example

**Scripts with sort keys:**
```
common/cast.sql         → sortKeys: ['10-common/0010']
common/text.sql         → sortKeys: ['10-common/0020']
core/foundation.sql     → sortKeys: ['20-internal/0000']
api/foundation.sql      → sortKeys: ['30-core/0000']
roles.sql               → sortKeys: ['00-bootstrap/0020', '50-seed/0010']
```

**Resulting execution order:**
```
1. roles.sql          (sort_key: 00-bootstrap/0020)  ← first phase
2. cast.sql           (sort_key: 10-common/0010)
3. text.sql           (sort_key: 10-common/0020)
4. foundation.sql     (sort_key: 20-internal/0000)
5. api/foundation.sql (sort_key: 30-core/0000)
6. roles.sql          (sort_key: 50-seed/0010)       ← second phase
```

### Implementation Location

See `internal/contract/api-v1.sql` for the view definition.
See `internal/scaffold/templates/advanced/deploy.sql` for usage in the execution loop.

---

## Advanced Template Architecture

### Four-Schema Design

The advanced template organizes code into four schemas by concern:

| Schema | Purpose | Ownership | Example Contents |
|--------|---------|-----------|------------------|
| **common** | Cross-cutting primitives (casting, encoding, text) | Database Owner | `try_cast()`, `slugify()`, `common.utf8` domain |
| **internal** | Infrastructure, deployment & test tracking | Database Owner | `deployment_script_execution_log`, `unittest_script`, `generate_test_script()` |
| **core** | Domain business logic | Database Owner | Your application tables, domain functions |
| **api** | External interface (HTTP routes, public API) | Database Owner | `handler` registry, protocol-specific route tables (`rest_route`, `rpc_route`, `mcp_route`), request handlers |

**Rationale:**
- **Separation of concerns:** Clear boundaries between infrastructure, utilities, domain, and interface
- **Security:** API schema grants execute-only to API role
- **Testability:** Utils have inline tests; API has integration tests
- **Maintainability:** Changes to domain logic don't affect routing infrastructure

**Privileges:**
```sql
-- Admin: Full access to all schemas
GRANT ALL ON SCHEMA common, internal, core, api TO <database>_admin;

-- API: Read-only + execute specific functions
GRANT USAGE ON SCHEMA common, api TO <database>_api;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO <database>_api;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO <database>_api;
```

### Three-Tier Role Hierarchy

| Role | Type | Purpose | Used By | Privileges |
|------|------|---------|---------|------------|
| **`<database>_owner`** | NOLOGIN | Object ownership | Nobody (only OWNED BY) | Owns all schemas, tables, functions |
| **`<database>_admin`** | LOGIN | Deployment & migrations | pgmi deploy, DBA tasks | Full CRUD on all schemas |
| **`<database>_api`** | LOGIN | Application connections | Application servers | EXECUTE api.*, SELECT read-only tables |

**Security Model:**
```sql
-- Owner never logs in (prevents direct manipulation)
CREATE ROLE myapp_owner NOLOGIN;

-- Admin for deployments
CREATE ROLE myapp_admin LOGIN PASSWORD '${database_admin_password}';
GRANT myapp_owner TO myapp_admin; -- Can act as owner

-- API is a NOLOGIN group role (a permission bundle); no password.
-- LOGIN roles (admin, customer) are GRANTed membership to inherit it.
CREATE ROLE myapp_api NOLOGIN;
GRANT USAGE ON SCHEMA api TO myapp_api;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO myapp_api;
```

**Why NOLOGIN owner?**
- Prevents accidental direct manipulation
- Forces all changes through admin role
- Clear separation: owner = identity, admin = actor
- Easier to audit (admin actions logged)

### HTTP Framework

The advanced template includes a built-in multi-protocol framework (REST, JSON-RPC, MCP) for PostgreSQL-backed APIs.

**Core Components:**

1. **Handler Registry** (`api.handler` table) — single table for all protocols, with
   `handler_type` enum distinguishing `rest` / `rpc` / `mcp_tool` / `mcp_resource` / `mcp_prompt`.
   Carries pg_proc snapshot columns (`returns_type`, `volatility`, `parallel`, `security`, etc.),
   `input_json_schema` / `output_json_schema` (`api.json_schema` domain), `response_headers`
   (jsonb), and `requires_auth`. Protocol-specific routing lives in sibling tables
   (`api.rest_route`, `api.rpc_route`, `api.mcp_route`).

2. **Handler Functions** — declared via registration helpers, not `INSERT`. Each helper
   validates the handler name (`^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$`), generates a random
   dollar-quote boundary, creates the function, captures pg_proc metadata, and links the
   route:
   ```sql
   SELECT api.create_or_replace_rest_handler(
       jsonb_build_object(
           'id',         'e1000001-0001-4000-8000-000000000001'::uuid,
           'uri',        '^/hello$',                  -- query string is stripped before match
           'httpMethod', '^GET$',
           'name',       'hello_world',
           'requiresAuth', false
       ),
       $body$
   BEGIN
       RETURN api.json_response(200, jsonb_build_object('message', 'Hello, World!'));
   END;
       $body$
   );
   ```

3. **Exchange Tables** (`api.rest_exchange`, `api.rpc_exchange`, `api.mcp_exchange`) —
   persistent request/response log, populated per invocation when `autoLog` is set.
   Exception paths record `sqlstate=<code> detail=<truncated>` rather than raw `SQLERRM`
   so attacker-supplied input does not leak through the log.

**Example Route Resolution:**

Routes are resolved inside `api.rest_invoke` / `api.rpc_invoke` / `api.mcp_call_tool`;
you do not `INSERT` directly. The registration helpers own the mapping.

**Request/Response Contract:**

**Request (input to handler):**
```json
{
  "method": "GET",
  "path": "/hello",
  "headers": {"Authorization": "Bearer token"},
  "query": {"name": "Alice"},
  "body": {}
}
```

**Response (output from handler):**
```json
{
  "status": 200,
  "headers": {"Content-Type": "application/json"},
  "body": {"message": "Hello, Alice!"}
}
```

**Use Cases:**
- PostgreSQL-backed REST APIs
- Database-driven routing (no application server config)
- Transactional API handlers (ACID guarantees)
- Versioned API evolution (routes in version control)

**See:** `api/examples.sql` for complete examples.

### Test Plan Persistence

pgmi provides `pgmi_persist_test_plan()` to persist the test execution plan to a permanent schema for CI/CD integration or auditing.

**Usage in deploy.sql:**
```sql
-- Persist test plan during deployment
SELECT pg_temp.pgmi_persist_test_plan('internal', NULL);  -- All tests
SELECT pg_temp.pgmi_persist_test_plan('internal', '/api/');  -- Filtered
```

**Auto-created Table Structure:**
```sql
CREATE TABLE <schema>.pgmi_test_plan (
    ordinal INT PRIMARY KEY,
    step_type TEXT NOT NULL,  -- 'fixture', 'test', 'teardown'
    script_path TEXT,         -- NULL for teardown steps
    directory TEXT NOT NULL,
    depth INT NOT NULL
);
```

**Query persisted test plan:**
```bash
psql -d mydb -c "SELECT ordinal, step_type, script_path FROM internal.pgmi_test_plan ORDER BY ordinal;"
```

**Use Cases:**
- CI/CD visibility into test execution order
- Audit trail of deployed tests
- External tooling integration

### HTTP Framework: Error Handling & Observability

The advanced template includes production-grade error handling with automatic classification, structured tracking, and distributed tracing support.

#### Error Classification Functions

Four helper functions provide intelligent error handling:

**1. `api.classify_sqlstate(sqlstate text) → text`**

Categorizes PostgreSQL errors into actionable classes:

```sql
CREATE OR REPLACE FUNCTION api.classify_sqlstate(sqlstate text)
RETURNS text AS $$
    SELECT CASE
        -- Transient errors (safe to retry)
        WHEN $1 LIKE '08%' THEN 'connection_failure'
        WHEN $1 IN ('40001', '40P01') THEN 'serialization_conflict'
        WHEN $1 = '55P03' THEN 'lock_timeout'
        WHEN $1 IN ('57014', '57P01') THEN 'query_timeout'

        -- Client errors (fix request and retry)
        WHEN $1 = '23505' THEN 'unique_violation'
        WHEN $1 = '23503' THEN 'foreign_key_violation'
        WHEN $1 = '23514' THEN 'check_violation'
        WHEN $1 = '23502' THEN 'not_null_violation'
        WHEN $1 LIKE '22%' THEN 'data_exception'

        -- Server errors (investigate)
        ELSE 'internal_error'
    END;
$$ LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE;
```

**Error Classes:**
- `connection_failure` - Database connection issues (transient)
- `serialization_conflict` - Deadlock or serialization failure (transient, retry)
- `lock_timeout` - Lock acquisition timeout (transient, retry with backoff)
- `query_timeout` - Statement timeout exceeded (transient or query issue)
- `unique_violation` - Duplicate key (client fix required)
- `foreign_key_violation` - Invalid FK reference (client fix required)
- `check_violation` - CHECK constraint failed (client fix required)
- `not_null_violation` - NULL in NOT NULL column (client fix required)
- `data_exception` - Invalid data format (client fix required)
- `internal_error` - Unexpected server error (investigate)

**2. `api.sqlstate_to_http_status(sqlstate text) → integer`**

Maps PostgreSQL errors to appropriate HTTP status codes:

```sql
CREATE OR REPLACE FUNCTION api.sqlstate_to_http_status(sqlstate text)
RETURNS integer AS $$
    SELECT CASE api.classify_sqlstate($1)
        WHEN 'connection_failure' THEN 503        -- Service Unavailable
        WHEN 'serialization_conflict' THEN 503    -- Service Unavailable (retry)
        WHEN 'lock_timeout' THEN 503              -- Service Unavailable (retry)
        WHEN 'query_timeout' THEN 504             -- Gateway Timeout
        WHEN 'unique_violation' THEN 409          -- Conflict
        WHEN 'foreign_key_violation' THEN 400     -- Bad Request
        WHEN 'check_violation' THEN 400           -- Bad Request
        WHEN 'not_null_violation' THEN 400        -- Bad Request
        WHEN 'data_exception' THEN 400            -- Bad Request
        ELSE 500                                  -- Internal Server Error
    END;
$$ LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE;
```

**Benefits:**
- Clients know whether to retry (503, 504) or fix request (400, 409)
- Distinguishes transient failures from client errors
- HTTP-compliant error responses (not everything is 500)

**3. `api.extract_trace_id(headers extensions.hstore) → text`**

Extracts correlation ID from standard tracing headers:

```sql
CREATE OR REPLACE FUNCTION api.extract_trace_id(headers extensions.hstore)
RETURNS text AS $$
    SELECT COALESCE(
        $1->'X-Trace-ID',
        $1->'X-Request-ID',
        $1->'X-Correlation-ID',
        gen_random_uuid()::text
    );
$$ LANGUAGE sql IMMUTABLE PARALLEL SAFE;
```

**Distributed Tracing:**
- Checks standard headers in order: `X-Trace-ID`, `X-Request-ID`, `X-Correlation-ID`
- Auto-generates UUID if not provided
- Enables request correlation across services, logs, and databases
- Essential for debugging distributed systems

**4. `api.build_error_context() → jsonb`**

Captures comprehensive error context using PostgreSQL's `GET STACKED DIAGNOSTICS`:

```sql
CREATE OR REPLACE FUNCTION api.build_error_context()
RETURNS jsonb AS $$
DECLARE
    v_sqlstate text;
    v_message text;
    v_detail text;
    v_hint text;
    v_context text;
BEGIN
    GET STACKED DIAGNOSTICS
        v_sqlstate = RETURNED_SQLSTATE,
        v_message = MESSAGE_TEXT,
        v_detail = PG_EXCEPTION_DETAIL,
        v_hint = PG_EXCEPTION_HINT,
        v_context = PG_EXCEPTION_CONTEXT;

    RETURN jsonb_build_object(
        'sqlstate', v_sqlstate,
        'error_class', api.classify_sqlstate(v_sqlstate),
        'message', v_message,
        'detail', v_detail,
        'hint', v_hint,
        'context', v_context,
        'timestamp', now()
    );
END;
$$ LANGUAGE plpgsql;
```

**Captured Data:**
- `sqlstate` - PostgreSQL error code (e.g., `23505` for unique violation)
- `error_class` - Classified category (e.g., `unique_violation`)
- `message` - Human-readable error message
- `detail` - Additional context (e.g., "Key (email)=(user@example.com) already exists")
- `hint` - Suggested resolution (e.g., "Use ON CONFLICT clause")
- `context` - Stack trace showing where error occurred
- `timestamp` - When error occurred

#### Observability Schema

**Enhanced `http_incoming_queue` Table:**

Three new columns provide comprehensive observability:

```sql
CREATE TABLE api.http_incoming_queue (
    -- ... existing columns ...
    error_history jsonb,              -- Structured error tracking
    trace_id text,                    -- Distributed tracing
    execution_time_ms numeric(10,2),  -- Performance metrics
    -- ... constraints ...
);
```

**Column Details:**

1. **`error_history jsonb`**
   - Array of structured error objects
   - Accumulates errors across retry attempts
   - Each entry contains full context from `build_error_context()`
   - Enables root cause analysis without log diving
   - GIN index for efficient JSON queries

2. **`trace_id text`**
   - Request correlation identifier
   - Extracted from standard headers or auto-generated
   - Links requests across services and logs
   - B-tree index for efficient filtering
   - Essential for distributed tracing

3. **`execution_time_ms numeric(10,2)`**
   - Handler execution time in milliseconds
   - Measured from handler start to completion
   - Excludes queue wait time
   - B-tree index (DESC) for performance analysis
   - Identifies slow handlers

**Indexes:**
```sql
-- Efficient JSON queries on error history
CREATE INDEX ix_http_incoming_queue_error_history
    ON api.http_incoming_queue USING gin(error_history)
    WHERE error_history IS NOT NULL;

-- Fast trace_id lookups for debugging
CREATE INDEX ix_http_incoming_queue_trace_id
    ON api.http_incoming_queue(trace_id)
    WHERE trace_id IS NOT NULL;

-- Performance analysis queries
CREATE INDEX ix_http_incoming_queue_execution_time
    ON api.http_incoming_queue(execution_time_ms DESC)
    WHERE execution_time_ms IS NOT NULL;
```

#### Enhanced Monitoring View

**`api.pvw_http_incoming_queue_messages()` - New Fields:**

```sql
RETURNS TABLE(
    -- ... existing fields ...
    trace_id text,                  -- Correlation identifier
    execution_time_ms numeric,      -- Performance metric
    error_history jsonb,            -- Full error context array
    error_count integer,            -- Total errors accumulated
    last_error_class text,          -- Classification of most recent error
    last_error_message text,        -- Human-readable last error
    last_sqlstate text,             -- PostgreSQL error code
    -- ... more fields ...
)
```

**Computed Fields:**
```sql
SELECT
    -- ... existing fields ...
    q.trace_id,
    q.execution_time_ms,
    q.error_history,
    COALESCE(jsonb_array_length(q.error_history), 0) as error_count,
    q.error_history->-1->>'error_class' as last_error_class,
    q.error_history->-1->>'message' as last_error_message,
    q.error_history->-1->>'sqlstate' as last_sqlstate,
    -- ... more fields ...
FROM api.http_incoming_queue q
-- ... joins and filters ...
```

#### Error Response Headers

When errors occur, `api.rest_invoke()` adds metadata to response headers:

```sql
v_response.headers := v_std_headers
    || extensions.hstore(ARRAY[
        'X-Route-Id', v_route.object_id::text,
        'X-Error-Class', v_error_context->>'error_class',
        'X-SQLSTATE', v_error_context->>'sqlstate',
        'X-Execution-Time-Ms', v_execution_ms::text,
        'X-Trace-ID', v_trace_id
    ]);
```

**Headers:**
- `X-Error-Class` - Error classification (e.g., `serialization_conflict`, `unique_violation`)
- `X-SQLSTATE` - PostgreSQL error code (e.g., `40P01`, `23505`)
- `X-Execution-Time-Ms` - Time spent before failure
- `X-Trace-ID` - Correlation identifier for distributed tracing
- `X-Route-Id` - Which route handler failed

**Client Benefits:**
- Know whether to retry (503/504) or fix request (400/409)
- Correlation ID for support requests
- Performance metrics for optimization
- Error classification for intelligent retry strategies

#### Debugging Examples

**Find all requests with errors in the last hour:**
```sql
SELECT
    trace_id,
    request_method,
    request_url,
    error_count,
    last_error_class,
    last_error_message,
    last_sqlstate,
    execution_time_ms
FROM api.pvw_http_incoming_queue_messages(interval '1 hour')
WHERE error_count > 0
ORDER BY enqueued_at DESC;
```

**Investigate specific error with full context:**
```sql
SELECT
    trace_id,
    error_history
FROM api.http_incoming_queue
WHERE trace_id = '<trace-id-from-logs>'
LIMIT 1;

-- Expand all error attempts
SELECT
    trace_id,
    jsonb_array_elements(error_history) as error_attempt
FROM api.http_incoming_queue
WHERE trace_id = '<trace-id-from-logs>';
```

**Analyze error patterns over 24 hours:**
```sql
SELECT
    error_history->-1->>'error_class' AS error_class,
    COUNT(*) AS occurrences,
    AVG(execution_time_ms) AS avg_execution_time,
    MAX(execution_time_ms) AS max_execution_time
FROM api.http_incoming_queue
WHERE error_history IS NOT NULL
  AND enqueued_at > now() - interval '24 hours'
GROUP BY error_history->-1->>'error_class'
ORDER BY occurrences DESC;
```

**Find slow requests (performance debugging):**
```sql
SELECT
    trace_id,
    request_method || ' ' || request_url AS endpoint,
    execution_time_ms,
    error_count
FROM api.pvw_http_incoming_queue_messages(interval '1 hour')
WHERE execution_time_ms > 1000  -- Slower than 1 second
ORDER BY execution_time_ms DESC
LIMIT 20;
```

**Track retry attempts for specific error class:**
```sql
SELECT
    trace_id,
    processing_attempts,
    jsonb_array_length(error_history) as error_count,
    error_history->0->>'timestamp' as first_error,
    error_history->-1->>'timestamp' as last_error,
    error_history->-1->>'error_class' as error_class
FROM api.http_incoming_queue
WHERE error_history IS NOT NULL
  AND error_history->-1->>'error_class' = 'serialization_conflict'
  AND enqueued_at > now() - interval '24 hours'
ORDER BY processing_attempts DESC;
```

#### Production Benefits

**HTTP Compliance:**
- Returns appropriate status codes (503, 504, 409, 400, not just 500)
- Clients can implement intelligent retry strategies
- RESTful error responses with proper semantics

**Distributed Tracing:**
- Correlation IDs link requests across services
- End-to-end visibility in distributed systems
- Support teams can trace issues across boundaries

**Structured Debugging:**
- Full error context captured (SQLSTATE, detail, hint, stack trace)
- No log diving required for root cause analysis
- Error history shows retry progression

**Performance Monitoring:**
- Execution time tracking per request
- Identify slow handlers and optimize
- Correlate errors with execution time

**Operational Excellence:**
- Error classification enables automated alerting (transient vs permanent)
- Accumulated error history shows patterns
- Observability built into the framework, not bolted on

**See:** `api/foundation.sql` (lines 771-1026 for helper functions, lines 1535-1780 for http_invoke error handling)

---

## Testing Templates

### Unit Testing (Go)

**Location:** `internal/scaffold/templates_unit_test.go`

**What to test:**
- Template directory structure exists
- Required files present (deploy.sql, README.md)
- No syntax errors in SQL files
- Metadata blocks parse correctly (advanced template)
- `{{PROJECT_NAME}}` variable is present where expected

**Example:**
```go
func TestAdvancedTemplateMetadata(t *testing.T) {
    content, err := templatesFS.ReadFile("templates/advanced/init.sql")
    require.NoError(t, err)

    // Verify metadata block exists
    assert.Contains(t, string(content), "<pgmi-meta")
    assert.Contains(t, string(content), "idempotent=")
}
```

### Scaffolding End-to-End Testing

**Location:** `internal/scaffold/integration_test.go`

**Note:** This tests the scaffolding workflow (`pgmi init` → deploy), not template SQL directly. Unlike template deployment tests (which MUST use EmbedFileSystem — see Step 5), scaffolding E2E tests legitimately use the real filesystem because they validate the `CreateProject` output.

**What to test:**
- Template initializes successfully
- All files copied correctly
- `{{PROJECT_NAME}}` substituted
- Deployment succeeds
- Tests execute successfully

**Preferred approach** (EmbedFileSystem — no real filesystem):
```go
func TestAdvancedTemplateDeployment(t *testing.T) {
    embedFS := scaffold.GetTemplatesFS()
    efs := filesystem.NewEmbedFileSystem(embedFS, "templates/advanced")
    deployer := testhelpers.NewTestDeployerWithFS(t, efs)
    err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
        ConnectionString: connString,
        DatabaseName:     "pgmi_test_advanced",
        SourcePath:       ".",
        Overwrite:        true,
        Force:            true,
        Parameters:       map[string]string{
            "database_admin_password":    "TestPass123!",
            "database_customer_password": "CustPass123!",
        },
    })
    require.NoError(t, err)
}
```

### SQL Testing (Template-Specific)

**Inline Tests (in common/):**
```sql
-- Immediate execution (not in __test__)
SELECT CASE
    WHEN common.try_cast('not-a-uuid', NULL::uuid) IS NULL
    THEN true
    ELSE (SELECT error('common.try_cast(text, null::uuid) should return NULL for invalid input'))
END;
```

**Transactional Tests (in __test__/):**
```sql
-- Executed via pgmi_test() macro, rolled back via savepoint
DO $$
DECLARE
    v_result UUID;
BEGIN
    -- Test idempotent re-execution
    INSERT INTO internal.deployment_script_execution_log (...) VALUES (...);

    -- Verify behavior
    IF NOT EXISTS (SELECT 1 FROM internal.deployment_script_execution_log WHERE ...) THEN
        RAISE EXCEPTION 'TEST FAILED: Script not tracked';
    END IF;
END $$;
```

---

## Troubleshooting

### Template Not Found

**Symptom:** `Error: template "my-template" not found`

**Causes:**
- Template directory not in `internal/scaffold/templates/`
- Typo in template name
- Template not embedded (need to rebuild CLI)

**Solution:**
```bash
# Verify template exists
ls internal/scaffold/templates/

# Rebuild CLI to embed new template
go build -o pgmi.exe ./cmd/pgmi

# List available templates
./pgmi.exe templates list
```

### {{PROJECT_NAME}} Not Substituted

**Symptom:** Literal `{{PROJECT_NAME}}` in deployed SQL

**Cause:** Variable substitution happens during `pgmi init`, not `pgmi deploy`

**Solution:** Re-initialize project (or manually replace in files)

### Metadata Parsing Errors

**Symptom:** `Error parsing metadata: invalid XML`

**Common Issues:**
- Missing closing tag (`</pgmi-meta>`)
- Invalid UUID format (not lowercase, wrong length)
- Typo in `idempotent` value (must be exactly `true` or `false`)
- Unclosed `<key>` tags

**Solution:**
```xml
<!-- ✗ BAD -->
<pgmi-meta id="ABC" idempotent="yes">

<!-- ✓ GOOD -->
<pgmi-meta
    id="a1b2c3d4-e5f6-7890-abcd-ef1234567890"
    idempotent="true">
```

### Scripts Executing in Wrong Order

**Symptom:** A script runs before its prerequisite

**Cause:** Sort keys not properly layered

**Solution:**
1. Review sort key conventions — prerequisites need lower sort keys
2. Use layer-based naming: `00-bootstrap/`, `10-common/`, `20-internal/`, `30-core/`, `40-api/`
3. Check `pgmi metadata plan ./myproject` to preview execution order

### Missing Required Parameters

**Symptom:** SQL error during deployment: `unrecognized configuration parameter "pgmi.database_admin_password"`

**Cause:** Template requires parameter not provided via `--param`

**Solution:**
```bash
# Advanced template requires these — pass via a params file, not on the
# command line (argv leaks to ps, shell history, and CI logs):
#   secrets.env:
#     database_admin_password=...
#     database_customer_password=...
pgmi deploy . --params-file secrets.env
```

**Find required params:** Check template's README.md "Required Parameters" section

### Tests Not Isolated

**Symptom:** Test files executed during deployment, modifying production data

**Cause:** Test files not in `__test__/` or `__tests__/` directory

**Solution:**
```bash
# ✗ BAD: Tests mixed with deployment files
migrations/
├── 001_schema.sql
└── 001_test_schema.sql  ← Will execute during deployment!

# ✓ GOOD: Tests isolated
migrations/
├── 001_schema.sql
└── __test__/
    └── test_schema.sql  ← Isolated, only executed via pgmi_test() macro
```

---

## Quick Reference

### Template File Checklist

- [ ] `deploy.sql` exists (required)
- [ ] `README.md` documents usage (recommended)
- [ ] Parameter contracts documented
- [ ] Test files in `__test__/` directories
- [ ] `{{PROJECT_NAME}}` used where appropriate
- [ ] Metadata blocks valid (if using advanced pattern)
- [ ] No circular dependencies
- [ ] Template tested end-to-end

### Metadata Block Template

```sql
/*
<pgmi-meta
    id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
    idempotent="true">
  <description>What this script does</description>
  <sortKeys>
    <key>00000000-0000-0000-0000-000000000000/0001</key>
  </sortKeys>
</pgmi-meta>
*/

-- Your SQL here
```

### Common Commands

```bash
# List available templates
pgmi templates list

# Describe template
pgmi templates describe advanced

# Initialize project
pgmi init myproject --template advanced

# Test template (after init) - tests run via pgmi_test() in deploy.sql
cd myproject
../pgmi deploy . -d test_db --param key=value --overwrite --force
```

---

## See Also

- **Basic Template README:** `internal/scaffold/templates/basic/README.md`
- **Advanced Template README:** `internal/scaffold/templates/advanced/README.md`
- **Scaffolder Implementation:** `internal/scaffold/scaffold.go`
- **CLI Commands:** `internal/cli/init.go`, `internal/cli/templates.go`
- **pgmi-sql skill:** SQL coding standards and helper functions
- **pgmi-deployment skill:** Execution internals and plan-based model

