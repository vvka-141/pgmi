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
- `internal/scaffold/scaffold.go` - Main scaffolder (251 lines)
- `internal/cli/init.go` - CLI command (123 lines)
- `internal/cli/templates.go` - Template discovery (178 lines)

---

## Available Templates

### Template Comparison

| Aspect | **basic** | **advanced** |
|--------|-----------|--------------|
| **Purpose** | Learning pgmi fundamentals | Production-ready deployments |
| **Structure** | Flat with `migrations/` folder | Domain-organized (utils/api/core/internal) |
| **Execution Model** | Directory-based phases | Metadata-driven topological sort |
| **Ordering** | Alphabetical (lexicographic) | Explicit dependencies + sort keys |
| **Tracking** | None (stateless) | UUID-based in `internal.deployment_script_execution_log` |
| **Metadata** | Optional/absent | Required `<pgmi-meta>` blocks |
| **Dependencies** | Implicit (directory structure) | Explicit (`<dependsOn>`, `<group>`) |
| **Schemas** | `public` (default) | Four-schema (utils/api/core/internal) |
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
├── init.sql                            # Role hierarchy & schemas
├── README.md                           # Comprehensive guide
├── api/
│   └── examples.sql                    # Example API handlers
├── lib/                                # Reusable library code
│   ├── api/                            # Multi-protocol API framework
│   │   ├── 01-types.sql                # Core types (http_request, http_response, etc.)
│   │   ├── 02-handler-registry.sql     # Handler metadata storage
│   │   ├── 03-rest-routes.sql          # REST route definitions
│   │   ├── 04-rpc-routes.sql           # JSON-RPC route definitions
│   │   ├── 05-mcp-routes.sql           # MCP protocol routes
│   │   ├── 06-queue-infrastructure.sql # Async task queue
│   │   ├── 07-helpers.sql              # Response builders, error helpers
│   │   ├── 08-registration.sql         # Handler registration functions
│   │   └── 09-gateways.sql             # Protocol gateway functions
│   ├── core/
│   │   └── foundation.sql              # Domain schema setup
│   ├── internal/
│   │   ├── foundation.sql              # Deployment tracking infrastructure
│   │   └── text-attributes.sql         # Text attribute utilities
│   ├── utils/
│   │   ├── cast_utils.sql              # Type conversion utilities
│   │   └── text_utils.sql              # String utilities with inline tests
│   └── __test__/                       # Library tests
│       ├── test_api_protocols.sql      # REST/RPC/MCP protocol tests
│       ├── test_auth_enforcement.sql   # Authentication tests
│       ├── test_error_handling.sql     # Error mapping tests
│       ├── test_handler_lifecycle.sql  # Handler registration tests
│       └── test_migrations_tracking.sql # Deployment tracking tests
└── __test__/
    └── README.md                       # Test documentation
```

**Key Features:**
- Metadata-driven execution (topological sort)
- UUID-based script tracking (survives renames)
- Four-schema architecture (internal, core, api, public)
- Three-tier role hierarchy (owner/admin/api)
- Multi-protocol API framework (REST, JSON-RPC, MCP)
- Authentication enforcement via handler metadata
- Inline testing patterns (in lib/utils/)
- Comprehensive deployment history
- Persistent test script tracking (`internal.unittest_script`)
- pgmi-independent test execution via `internal.generate_test_script()`

**Parameter Contract:**
- `database_admin_password` (REQUIRED) - Password for admin role
- `database_api_password` (optional) - Password for API role (defaults to generated)
- `env` (optional) - Environment identifier (dev/staging/prod)

**Use Cases:**
- Production database deployments
- Multi-schema applications
- API-first database design
- Complex dependency management
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
- `deploy.sql` - Must exist. Orchestrates deployment by populating `pg_temp.pgmi_plan`.

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
pgmi deploy . \
  --param database_owner=myapp_owner \
  --param admin_password="SecurePass123!"
```
```

**Parameter Validation:**
- Currently: No automatic validation (user gets runtime SQL errors if missing)
- Roadmap: Template-level parameter contracts with CLI validation

**Best Practice:**
- Use `pgmi_get_param('key', 'default')` in deploy.sql for optional params
- Use `current_setting('pgmi.key')` for required params (fails if missing)

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
- Topological sort by dependencies
- Idempotency tracking
- Execution order calculation
- Cycle detection

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
- [ ] deploy.sql populates `pg_temp.pgmi_plan` successfully
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

The advanced template uses XML metadata blocks to declare script identity, dependencies, and execution characteristics.

### Metadata Block Structure

```sql
/*
<pgmi-meta
    id="UUID"
    idempotent="true|false">
  <description>Human-readable purpose of this file</description>
  <sortKeys>
    <key>layer-uuid/sequence-number</key>
    <key>another-layer-uuid/sequence-number</key>
  </sortKeys>
  <membership>
    <group id="group-uuid"/>
  </membership>
  <dependency>
    <dependsOn id="prerequisite-uuid"/>
    <dependsOn id="another-prerequisite-uuid"/>
  </dependency>
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
  - Referenced by other scripts via `<dependsOn>`
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
- **Purpose:** Control execution order within dependency graph
- **Format:** `<layer-uuid>/<sequence-number>`
  - `layer-uuid`: Groups scripts into execution layers
  - `sequence-number`: Relative ordering within layer (0001, 0002, etc.)
- **Behavior:**
  - Scripts with lower sequence numbers execute first
  - Multiple sort keys provide multi-level ordering
- **Example:**
  ```xml
  <sortKeys>
    <key>00000000-0000-0000-0000-000000000000/0001</key>
    <key>11111111-1111-1111-1111-111111111111/0010</key>
  </sortKeys>
  ```
- **Use Cases:**
  - Ensure foundation scripts run before domain scripts
  - Order within schema (utils → internal → core → api)

#### `<membership>` (OPTIONAL)
- **Type:** List of `<group>` elements
- **Purpose:** Declare script belongs to logical groups
- **Format:** `<group id="uuid"/>`
- **Usage:** Future extensibility (filtering, conditional execution)
- **Example:**
  ```xml
  <membership>
    <group id="dddddddd-dddd-dddd-dddd-dddddddddddd"/>
  </membership>
  ```

#### `<dependency>` (OPTIONAL)
- **Type:** List of `<dependsOn>` elements
- **Purpose:** Declare explicit prerequisites (must execute before this script)
- **Format:** `<dependsOn id="prerequisite-uuid"/>`
- **Behavior:**
  - Creates directed edge in dependency graph
  - Topological sort ensures prerequisites run first
  - Cycle detection prevents circular dependencies
- **Example:**
  ```xml
  <dependency>
    <dependsOn id="a1b2c3d4-e5f6-7890-abcd-ef1234567890"/>
    <dependsOn id="b2c3d4e5-f6a7-8901-bcde-f12345678901"/>
  </dependency>
  ```

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
  <membership>
    <group id="dddddddd-dddd-dddd-dddd-dddddddddddd"/>
  </membership>
  <dependency>
    <dependsOn id="a1b2c3d4-e5f6-7890-abcd-ef1234567890"/>
  </dependency>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION utils.slugify(input_text TEXT)
RETURNS TEXT AS $$
BEGIN
    RETURN lower(regexp_replace(input_text, '[^a-zA-Z0-9]+', '-', 'g'));
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- Inline test (non-transactional, runs immediately)
SELECT CASE
    WHEN utils.slugify('Hello World!') = 'hello-world-'
    THEN true
    ELSE (SELECT error('Slugify test failed'))
END;
```

---

## Dependency Resolution Algorithm

The advanced template's `deploy.sql` implements topological sorting to determine execution order.

### Algorithm Overview

**Input:** Set of scripts with metadata (`id`, `dependsOn`, `sortKeys`)
**Output:** Linear execution order respecting dependencies

**Steps:**

1. **Build Dependency Graph**
   ```sql
   -- For each script, extract:
   -- - id (node identifier)
   -- - dependsOn IDs (incoming edges)
   -- - sortKeys (ordering hints)
   ```

2. **Detect Cycles**
   ```sql
   -- Depth-first search for back edges
   -- If cycle found: RAISE EXCEPTION with cycle path
   ```

3. **Topological Sort (Kahn's Algorithm)**
   ```sql
   -- Initialize: Find all nodes with no incoming edges
   -- While queue not empty:
   --   1. Dequeue node with lowest sort_key
   --   2. Add to execution order (assign execution_order++)
   --   3. Remove node from graph
   --   4. Enqueue newly-freed nodes (whose dependencies now satisfied)
   ```

4. **Assign Execution Order**
   ```sql
   -- Populate pg_temp.pgmi_plan with scripts in topological order
   -- execution_order: 1, 2, 3, ... N
   ```

### Pseudo-code

```
function topological_sort(scripts):
    graph = build_graph(scripts)

    if has_cycle(graph):
        raise error("Circular dependency detected")

    in_degree = calculate_in_degrees(graph)
    queue = [node for node in graph if in_degree[node] == 0]
    execution_order = []

    while queue not empty:
        # Sort by sort_keys for deterministic ordering
        queue.sort(by=sort_keys)

        node = queue.dequeue()
        execution_order.append(node)

        for neighbor in graph[node].outgoing_edges:
            in_degree[neighbor] -= 1
            if in_degree[neighbor] == 0:
                queue.enqueue(neighbor)

    if len(execution_order) != len(graph):
        raise error("Graph has cycles (should have been caught earlier)")

    return execution_order
```

### Example Dependency Graph

**Scripts:**
```
A: init.sql (no dependencies)
B: utils/text_utils.sql (depends on A)
C: utils/cast_utils.sql (depends on A)
D: internal/foundation.sql (depends on A)
E: core/foundation.sql (depends on D)
F: api/foundation.sql (depends on D, E)
```

**Graph:**
```
A → B
A → C
A → D → E → F
     ↓
     F
```

**Execution Order:**
```
1. A (no dependencies)
2. B, C, D (all depend only on A, sorted by sort_keys)
3. E (depends on D)
4. F (depends on D and E)
```

### Implementation Location

See `internal/scaffold/templates/advanced/deploy.sql` (lines 50-280) for complete implementation with:
- Metadata parsing from `<pgmi-meta>` blocks
- Dependency graph construction
- Cycle detection with path reporting
- Topological sort with sort_key tie-breaking

---

## Advanced Template Architecture

### Four-Schema Design

The advanced template organizes code into four schemas by concern:

| Schema | Purpose | Ownership | Example Contents |
|--------|---------|-----------|------------------|
| **utils** | Pure utility functions (no side effects) | Database Owner | `try_cast_uuid()`, `slugify()`, text/date helpers |
| **internal** | Infrastructure, deployment & test tracking | Database Owner | `deployment_script_execution_log`, `unittest_script`, `generate_test_script()` |
| **core** | Domain business logic | Database Owner | Your application tables, domain functions |
| **api** | External interface (HTTP routes, public API) | Database Owner | `http_route`, request handlers, queues |

**Rationale:**
- **Separation of concerns:** Clear boundaries between infrastructure, utilities, domain, and interface
- **Security:** API schema grants execute-only to API role
- **Testability:** Utils have inline tests; API has integration tests
- **Maintainability:** Changes to domain logic don't affect routing infrastructure

**Privileges:**
```sql
-- Admin: Full access to all schemas
GRANT ALL ON SCHEMA utils, internal, core, api TO <database>_admin;

-- API: Read-only + execute specific functions
GRANT USAGE ON SCHEMA utils, api TO <database>_api;
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

-- API for application (least privilege)
CREATE ROLE myapp_api LOGIN PASSWORD '${database_api_password}';
GRANT USAGE ON SCHEMA api TO myapp_api;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO myapp_api;
```

**Why NOLOGIN owner?**
- Prevents accidental direct manipulation
- Forces all changes through admin role
- Clear separation: owner = identity, admin = actor
- Easier to audit (admin actions logged)

### HTTP Framework

The advanced template includes a built-in HTTP routing framework for PostgreSQL-backed APIs.

**Core Components:**

1. **Route Registry** (`api.http_route` table)
   ```sql
   CREATE TABLE api.http_route (
       object_id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
       route_name           TEXT NOT NULL UNIQUE,
       handler_function_name TEXT NOT NULL,
       address_regexp       TEXT NOT NULL, -- POSIX regex for URL
       method_regexp        TEXT NOT NULL, -- POSIX regex for HTTP method
       sequence_number      INT NOT NULL,
       auto_log             BOOLEAN DEFAULT true,
       volatility           TEXT DEFAULT 'VOLATILE',
       language_name        TEXT DEFAULT 'plpgsql'
   );
   ```

2. **Handler Functions** (user-defined)
   ```sql
   CREATE FUNCTION api.handle_hello_world(p_request jsonb)
   RETURNS jsonb AS $$
   BEGIN
       RETURN jsonb_build_object(
           'status', 200,
           'headers', jsonb_build_object('Content-Type', 'application/json'),
           'body', jsonb_build_object('message', 'Hello, World!')
       );
   END;
   $$ LANGUAGE plpgsql SECURITY DEFINER;
   ```

3. **Request Queue** (optional, for async processing)
   ```sql
   CREATE TABLE api.http_request_queue (
       request_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
       route_name TEXT NOT NULL,
       request_data jsonb NOT NULL,
       created_at TIMESTAMPTZ DEFAULT now(),
       processed_at TIMESTAMPTZ,
       status TEXT DEFAULT 'pending'
   );
   ```

**Example Route Definition:**
```sql
INSERT INTO api.http_route (
    route_name,
    handler_function_name,
    address_regexp,
    method_regexp,
    sequence_number
) VALUES (
    'hello_world',
    'api.handle_hello_world',
    '^/hello/?$',          -- Matches /hello or /hello/
    '^GET$',               -- Only GET requests
    100
);
```

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

**See:** `api/foundation.sql`, `api/hello-world.sql` for complete examples.

### Test Script Persistence

The advanced template persists test scripts to `internal.unittest_script` during deployment, enabling pgmi-independent test execution.

**Table Structure:**
```sql
CREATE TABLE internal.unittest_script (
    execution_order INT NOT NULL PRIMARY KEY,
    step_type TEXT NOT NULL CHECK (step_type IN ('setup', 'test', 'teardown')),
    script_path TEXT NOT NULL,
    script_directory TEXT NOT NULL,
    savepoint_id TEXT NOT NULL,
    content TEXT NOT NULL,
    deployed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deployed_by TEXT NOT NULL DEFAULT CURRENT_USER
);
```

**Generator Function:**
```sql
-- Generate executable test script for all tests
SELECT internal.generate_test_script();

-- Generate script for filtered tests (POSIX regex)
SELECT internal.generate_test_script('/api/');
```

**Power User Workflow:**
```bash
# Inspect deployed tests
psql -d mydb -c "SELECT execution_order, step_type, script_path FROM internal.unittest_script ORDER BY execution_order;"

# Generate and execute all tests (shows NOTICE messages)
psql -d mydb -tA -c "SELECT internal.generate_test_script();" | psql -d mydb
```

**Benefits:**
- Decouple deployed databases from pgmi tooling
- Use standard psql for test execution
- Integrate with CI/CD pipelines without pgmi
- Debug with PostgreSQL's native error messages

**See:** `internal/foundation.sql` for generator implementation, `deploy.sql` for persistence logic.

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

When errors occur, `api.http_invoke()` adds metadata to response headers:

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
            "database_admin_password": "TestPass123!",
            "database_api_password":   "ApiPass123!",
        },
    })
    require.NoError(t, err)
}
```

### SQL Testing (Template-Specific)

**Inline Tests (in utils/):**
```sql
-- Immediate execution (not in __test__)
SELECT CASE
    WHEN utils.try_cast_uuid('not-a-uuid') IS NULL
    THEN true
    ELSE (SELECT error('try_cast_uuid should return NULL for invalid input'))
END;
```

**Transactional Tests (in __test__/):**
```sql
-- Executed during `pgmi test`, rolled back after
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
- Unclosed `<dependsOn>` or `<key>` tags

**Solution:**
```xml
<!-- ✗ BAD -->
<pgmi-meta id="ABC" idempotent="yes">

<!-- ✓ GOOD -->
<pgmi-meta
    id="a1b2c3d4-e5f6-7890-abcd-ef1234567890"
    idempotent="true">
```

### Circular Dependency Detected

**Symptom:** `Error: Circular dependency detected: A → B → C → A`

**Cause:** Scripts have cyclic `<dependsOn>` references

**Solution:**
1. Review dependency graph
2. Remove unnecessary dependencies
3. Refactor to break cycle (extract common dependency)

**Example:**
```
A depends on B
B depends on C
C depends on A  ← Cycle!

Refactor:
D (new common foundation)
A depends on D
B depends on D
C depends on D
```

### Missing Required Parameters

**Symptom:** SQL error during deployment: `unrecognized configuration parameter "pgmi.database_admin_password"`

**Cause:** Template requires parameter not provided via `--param`

**Solution:**
```bash
# Advanced template requires these
pgmi deploy . \
  --param database_admin_password="SecurePass123!" \
  --param database_api_password="ApiPass123!"
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
    └── test_schema.sql  ← Isolated, only executed via `pgmi test`
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
  <dependency>
    <dependsOn id="prerequisite-uuid"/>
  </dependency>
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

# Test template (after init)
cd myproject
../pgmi deploy . -d test_db --param key=value --overwrite --force
../pgmi test . -d test_db
```

---

## See Also

- **Basic Template README:** `internal/scaffold/templates/basic/README.md`
- **Advanced Template README:** `internal/scaffold/templates/advanced/README.md`
- **Scaffolder Implementation:** `internal/scaffold/scaffold.go`
- **CLI Commands:** `internal/cli/init.go`, `internal/cli/templates.go`
- **pgmi-sql skill:** SQL coding standards and helper functions
- **pgmi-deployment skill:** Execution internals and plan-based model

