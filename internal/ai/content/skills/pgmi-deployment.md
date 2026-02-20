---
name: pgmi-deployment
description: "Use when working on deployer or plan execution"
user_invocable: true
---

**Use this skill when:**
- Understanding pgmi's execution model and architecture
- Working on the deployer service implementation
- Debugging deployment issues or unexpected behavior
- Extending the execution engine
- Understanding how files are discovered, filtered, and executed
- Working with checksums, parameters, or the plan table

---

## Core Execution Model

### Session-Centric Architecture

**Principle:** All deployment work happens in a **single PostgreSQL session**.

This design provides:
- ✅ **Transactional safety** - All work can be wrapped in one transaction
- ✅ **Isolation** - Temporary tables exist only for this deployment
- ✅ **Simplicity** - No external state management
- ✅ **Inspectability** - All state queryable during the session; clear boundaries for logging

**Contrast with other tools:**
- ❌ Multi-session tools: State stored in files, harder to reason about
- ❌ External orchestrators: Deployment state outside database
- ✅ pgmi: Everything in one session, deterministic and atomic

---

## Deployment Lifecycle

### Phase 1: Connection & Preparation

**Step 1: Establish Connection**
```
User runs: pgmi deploy ./myapp -d mydb

pgmi:
1. Parses connection string (PostgreSQL URI or ADO.NET format)
2. Creates Connector via ConnectionProvider factory
3. Establishes single session to maintenance database
4. If --db flag: Creates or recreates target database
5. Reconnects to target database for deployment
```

**Step 2: Create Session Infrastructure**

pgmi creates temporary tables and helper functions in `pg_temp` schema:

**Internal Tables** (underscore prefix):
```sql
-- pg_temp._pgmi_source
-- Contains all discovered files EXCEPT deploy.sql and test files
CREATE TEMP TABLE _pgmi_source (
    path TEXT NOT NULL,              -- File path (e.g., ./migrations/001.sql)
    directory TEXT NOT NULL,          -- Parent directory with trailing / (e.g., ./migrations/)
    name TEXT NOT NULL,              -- Filename extracted from path
    content TEXT NOT NULL,            -- File contents
    checksum TEXT NOT NULL,           -- Raw checksum (exact content hash)
    pgmi_checksum TEXT NOT NULL,     -- Normalized checksum (content identity)
    is_sql_file BOOLEAN NOT NULL,   -- True if extension is a recognized SQL type
    -- Additional metadata fields
);

-- pg_temp._pgmi_parameter
-- Contains CLI-supplied parameters
CREATE TEMP TABLE _pgmi_parameter (
    key TEXT NOT NULL,
    value TEXT NOT NULL
);

-- pg_temp._pgmi_test_source
-- Test files isolated from deployment
CREATE TEMP TABLE _pgmi_test_source (
    path TEXT NOT NULL,
    content TEXT NOT NULL,
    -- Same structure as _pgmi_source
);
```

**Public Views** (stable API for deploy.sql):
```sql
-- pg_temp.pgmi_source_view, pgmi_parameter_view, pgmi_test_source_view
-- pg_temp.pgmi_plan_view - Execution order with metadata JOIN
```

**Helper Functions:**
```sql
-- Test plan (returns execution order for tests)
pg_temp.pgmi_test_plan(p_pattern TEXT DEFAULT NULL) RETURNS TABLE(ordinal INT, step_type TEXT, script_path TEXT, directory TEXT, depth INT)

-- Test discovery
pg_temp.pgmi_has_tests(p_directory TEXT, p_pattern TEXT DEFAULT NULL) RETURNS BOOLEAN
```

**Parameter Access:**
Parameters passed via `--param key=value` are automatically available as session configuration variables using the `pgmi.` namespace prefix. Access them using PostgreSQL's native `current_setting()` function:
```sql
-- Access parameter with fallback to default (returns NULL if not set when true)
SELECT COALESCE(current_setting('pgmi.env', true), 'development');

-- Access required parameter (fails if not set)
SELECT current_setting('pgmi.database_admin_password');
```

Templates can define their own helper functions for parameter access if desired (e.g., `deployment_setting()` in the advanced template).

**Preprocessor Macro:**
```sql
-- CALL pgmi_test() is a preprocessor macro expanded by Go (not a SQL function)
-- Syntax: CALL pgmi_test(); or CALL pgmi_test('./pattern/**');
-- Generates inline SQL with savepoint isolation via pgmi_test_generate()
```

---

### Phase 2: File Discovery & Population

**Step 3: Discover SQL Files**

pgmi recursively scans the provided path for SQL files:

**Discovery Rules:**
1. **Include:** All SQL files in provided directory (recursive). Recognized extensions: `.sql`, `.ddl`, `.dml`, `.dql`, `.dcl`, `.psql`, `.pgsql`, `.plpgsql`. The `is_sql_file` column in `pgmi_source_view` indicates whether a file matches these extensions.
2. **Exclude:** `deploy.sql` (executed separately by pgmi, never loaded into internal tables)
3. **Exclude:** Files in `__test__/` and `__tests__/` directories (loaded directly into `_pgmi_test_source`, never into `_pgmi_source`)
4. **Order:** Files discovered in filesystem order (no guarantee)

**File Categorization:**
```
./myapp/
├── deploy.sql                    → Executed separately by pgmi (NOT in _pgmi_source)
├── init.sql                      → pg_temp._pgmi_source (access via pgmi_source_view)
├── migrations/
│   ├── 001_schema.sql           → pg_temp._pgmi_source (access via pgmi_source_view)
│   └── 002_data.sql             → pg_temp._pgmi_source (access via pgmi_source_view)
├── utils/
│   └── helpers.sql              → pg_temp._pgmi_source (access via pgmi_source_view)
└── __test__/
    ├── test_schema.sql          → pg_temp._pgmi_test_source (ISOLATED)
    └── test_data.sql            → pg_temp._pgmi_test_source (ISOLATED)
```

**Why Isolate Tests?**

Test files often manipulate schema and data for testing purposes. If executed during deployment by mistake, they can cause **unrecoverable data corruption**.

**Isolation Strategy:**
- Physical separation: Different internal tables (`_pgmi_source` vs `_pgmi_test_source`)
- Cannot accidentally execute via `pgmi_plan_view` (only joins `_pgmi_source`, not `_pgmi_test_source`)
- Only accessible via `CALL pgmi_test()` preprocessor macro in deploy.sql
- Automatic: Developer cannot bypass

**Step 4: Calculate Checksums**

For each discovered file, pgmi calculates **two checksums**:

**1. Raw Checksum** (exact content hash)
```go
// Pseudocode
rawChecksum := sha256(fileContent)
```
- Purpose: Detect **any** change (whitespace, comments, etc.)
- Use case: Strict change detection

**2. Normalized Checksum** (content identity)
```go
// Pseudocode
normalized := fileContent
normalized = toLowerCase(normalized)
normalized = removeComments(normalized)  // -- and /* */
normalized = collapseWhitespace(normalized)  // Multiple spaces → single space
pgmiChecksum := sha256(normalized)
```
- Purpose: Track script **identity** independent of formatting
- Use case: Rename detection, idempotency, tracking

**Example:**
```sql
-- Original file (001.sql)
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    -- User's email
    email TEXT NOT NULL
);

-- Same content, different formatting (001_renamed.sql)
create table users(id serial primary key,email text not null);
```
- **Raw checksum:** Different (whitespace/comments changed)
- **Normalized checksum:** Same (functional content identical)

**Usage in Advanced Template:**
- `script_id` (UUID) + `pgmi_checksum` = unique script identity
- Script survives rename: UUID stays same, `pgmi_checksum` unchanged
- Content change detected: `pgmi_checksum` differs
- Idempotent scripts re-execute if `pgmi_checksum` changed

---

### Phase 3: Execute deploy.sql

**Step 5: Run deploy.sql**

pgmi executes `deploy.sql` directly. Your deploy.sql queries files from `pgmi_plan_view` and uses `EXECUTE` to run them.

**deploy.sql Responsibilities:**
- ✅ Transaction boundaries (BEGIN/COMMIT at top level)
- ✅ Execution order (query `pgmi_plan_view ORDER BY execution_order`)
- ✅ Conditional logic (environment-specific behavior via PL/pgSQL)
- ✅ Error handling (BEGIN...EXCEPTION blocks inside DO)
- ✅ Testing strategy (use `pgmi_test()` macro)

**pgmi Responsibilities:**
- ✅ Connection management
- ✅ File discovery and checksumming
- ✅ Temporary table setup
- ✅ Preprocessor macro expansion (`pgmi_test()`)
- ✅ Real-time output (RAISE NOTICE)

**Example deploy.sql (Basic):**
```sql
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Run tests (preprocessor macro)
CALL pgmi_test();

COMMIT;
```

**Example deploy.sql (Advanced - Metadata-Driven):**
```sql
-- See pgmi-templates skill for complete metadata-driven example
-- Includes:
-- - Metadata parsing from <pgmi-meta> blocks via pgmi_source_metadata
-- - Multi-phase execution via sort_keys
-- - Idempotency tracking in internal.deployment_script_execution_log
```

---

### Phase 4: Direct Execution

**Key Characteristics:**
- **Direct:** `deploy.sql` runs your SQL directly via `EXECUTE v_file.content`
- **Fail-fast:** First error stops deployment immediately
- **Transparent:** All SQL executed in current session (visible to user)
- **Real-time:** Progress via PostgreSQL's `RAISE NOTICE` mechanism

**Error Handling:**
- pgmi does NOT retry or handle errors
- Errors propagate to user immediately
- Rollback behavior determined by transaction strategy in deploy.sql
- Exception handling belongs in deploy.sql (user's domain)

---

### Phase 5: Completion

**Step 7: Session Cleanup**

When deployment finishes (success or failure):

1. **Temporary tables drop automatically** (session-scoped)
2. Session ends
3. CLI returns exit code:
   - `0` = Success
   - Non-zero = Failure (with error message)

**No persistent state outside database:**
- No state files on disk
- No deployment logs outside PostgreSQL
- All tracking happens in user's schema (if implemented, e.g., `internal.deployment_script_execution_log`)

---

## Parameter System

### Session Configuration Variables

pgmi parameters are automatically available as PostgreSQL session configuration variables without manual initialization.

**Automatic Initialization:**
- CLI parameters (`--param key=value`) automatically become session configuration variables
- No manual initialization needed - parameters are immediately accessible
- Session variables use `pgmi.` namespace prefix
- Accessible via `current_setting('pgmi.key', true)` with COALESCE for defaults

**Parameter Access Pattern:**
Use PostgreSQL's native `current_setting()` function:
```sql
-- Optional parameter with default
COALESCE(current_setting('pgmi.env', true), 'development')

-- Required parameter (fails if not set)
current_setting('pgmi.database_admin_password')
```

**Template-Specific Helpers:**
Templates can define their own helper functions for parameter access with defaults, validation, and type coercion. For example, the advanced template provides `deployment_setting()`.

**Example:**
```sql
-- deploy.sql - Access parameters directly
DO $$
DECLARE
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
BEGIN
    IF v_env = 'production' THEN
        RAISE NOTICE 'Production deployment detected';
    END IF;

    -- Required parameter - fails fast if missing
    PERFORM current_setting('pgmi.database_admin_password');
END $$;
```

**CLI Usage:**
```bash
# Basic deployment with parameters
pgmi deploy . --database mydb --param env=production --param max_connections=200

# Required parameter missing (fails when accessed in deploy.sql)
pgmi deploy . --database mydb
# ERROR: unrecognized configuration parameter "pgmi.database_admin_password"
```

**Security Note:**
- Session variables visible via `SHOW ALL` and `pg_settings`
- Do NOT pass secrets (passwords, API keys, tokens) as parameters
- Use connection strings for sensitive credentials
- Session variables exist only for deployment session

---

## Direct Execution Model

### How It Works

**The Contract:**
1. deploy.sql queries files from `pg_temp.pgmi_plan_view`
2. deploy.sql executes files directly with `EXECUTE v_file.content`
3. Transparent: file content passes through unmodified (except `CALL pgmi_test()` macro expansion)

**Core Pattern:**

```sql
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
```

**Why This Model?**

**Advantages:**
- ✅ **Transparent:** User controls exactly what executes
- ✅ **Deterministic:** Same inputs → same execution order → same result
- ✅ **Flexible:** User controls deployment logic (transaction boundaries, filtering, conditionals)
- ✅ **Simple:** No queue abstraction, just SQL executing SQL
- ✅ **PostgreSQL-native:** Uses standard PL/pgSQL patterns

**Trade-offs:**
- ⚠️ User responsible for correctness (pgmi doesn't validate)
- ⚠️ No retry/error taxonomy (user handles in deploy.sql)
- ⚠️ No implicit idempotency (user implements if needed)

**Philosophy:** pgmi is an **execution fabric**, not a migration framework. Orchestration belongs to user's SQL.

---

## Test Isolation Mechanism

### Why Isolation?

**Problem:**
Test files often:
- Drop/recreate tables for testing
- Insert test data
- Manipulate schema temporarily
- Test failure scenarios

**Risk:**
If executed during deployment by mistake → **data corruption**

**Solution:**
Physical isolation in separate temp table (`pg_temp.pgmi_test_source`)

### How It Works

**File Discovery Phase:**
```
1. Scan directory for all files (recursive)
2. Skip deploy.sql (executed separately by pgmi)
3. Check path contains __test__ or __tests__
4. If yes → insert into pgmi_test_source (never into pgmi_source)
5. If no  → insert into pgmi_source (with is_sql_file = true/false based on extension)
```

**During Deployment:**
- `pgmi_plan_view` only includes files from `pgmi_source`
- Cannot accidentally execute test files
- Test files invisible to deployment logic

**During Testing:**
- `CALL pgmi_test()` macro reads from `_pgmi_test_source` via `pgmi_test_plan()` function
- Can filter by regex pattern
- Executes in transaction with automatic rollback

**Example:**
```sql
-- In deploy.sql
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- This loop CANNOT see test files (they are in pgmi_test_source, not pgmi_source)
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Run tests (preprocessor macro handles savepoints automatically)
CALL pgmi_test();

COMMIT;
```

---

## Operational Modes & Transaction Strategies

### Single-Transaction Mode

**Pattern:** Wrap entire deployment in one transaction

**Benefits:**
- ✅ Maximum atomicity (all-or-nothing)
- ✅ Simple rollback on failure
- ✅ Consistent snapshot

**Trade-offs:**
- ⚠️ Long lock duration
- ⚠️ High contention on busy systems
- ⚠️ Large rollback cost if failure near end

**Example:**
```sql
-- deploy.sql: Single transaction
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
```

**When to use:**
- Small deployments (<1 minute)
- Low-traffic databases
- Critical consistency requirements
- Acceptable downtime window

---

### Phased Commits

**Pattern:** Commit per phase (pre-deployment → migrations → setup → post-deployment)

**Benefits:**
- ✅ Limited lock scope
- ✅ Partial progress visible
- ✅ Lower contention
- ✅ Smaller rollback cost per phase

**Trade-offs:**
- ⚠️ Less atomicity (phase 1 succeeds, phase 2 fails = partial deployment)
- ⚠️ Need compensating logic for failures
- ⚠️ More complex orchestration

**Example:**
```sql
-- deploy.sql: Phased commits (each phase commits separately)

-- Phase 1: Schema foundation
BEGIN;
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_plan_view
        WHERE path = './init.sql'
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;

-- Phase 2: Migrations (heavyweight DDL)
BEGIN;
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;

-- Phase 3: Setup (idempotent functions, views)
BEGIN;
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './setup/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;

-- Phase 4: Tests (rolled back via CALL pgmi_test() macro)
BEGIN;
CALL pgmi_test();
COMMIT;
```

**When to use:**
- Long-running deployments (>1 minute)
- High-traffic databases
- Need to limit lock contention
- Acceptable partial deployment state

---

### Locking Posture

**pgmi's Position:**
- pgmi does NOT alter PostgreSQL's native locking behavior
- Lock scope and duration follow user's transaction strategy
- PostgreSQL's default locking applies (DDL takes locks, DML follows MVCC)

**User Controls:**
- Transaction boundaries (BEGIN/COMMIT in deploy.sql)
- Lock timeout settings (`SET lock_timeout = '10s'`)
- Statement timeout (`SET statement_timeout = '30s'`)
- Isolation level (default: READ COMMITTED)

**Recommendations:**
- **Heavyweight DDL** (ALTER TABLE, CREATE INDEX): Isolate in early phase, commit before traffic-heavy operations
- **Idempotent setup** (CREATE OR REPLACE FUNCTION): Can run in later phase, low lock contention
- **Long-running queries**: Use phased commits to limit blast radius
- **Production deployments**: Consider `lock_timeout` to fail fast if lock unavailable

**Example: Lock-Aware Deployment**
```sql
-- deploy.sql: Lock-aware phased deployment

-- Phase 1: Heavyweight DDL (early, low traffic)
SET lock_timeout = '10s';
BEGIN;
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_plan_view
        WHERE path = './migrations/001_alter_users_table.sql'
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
RESET lock_timeout;

-- Phase 2: Lightweight setup (idempotent, low contention)
BEGIN;
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './functions/%'
        ORDER BY execution_order
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
```

---

## Execution Order & Determinism

### How Order is Determined

**pgmi's Guarantee:**
- `pgmi_plan_view` provides `execution_order` column (based on sort_keys and path)
- Order deterministic given same files and metadata
- Files without metadata use path as sort key (lexicographic order)

**User's Responsibility:**
- deploy.sql queries `pgmi_plan_view ORDER BY execution_order`
- Can use WHERE clauses, conditionals, additional sorting for custom order
- Advanced: Use `<pgmi-meta>` sort_keys for multi-phase execution

**Example: Explicit Ordering**
```sql
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- Order controlled by execution_order (from pgmi_plan_view)
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
```

**Non-Determinism Sources:**
- ❌ `SELECT path FROM pg_temp.pgmi_source_view` without ORDER BY (filesystem order)
- ❌ Hash-based iteration in PL/pgSQL
- ✅ Always use `ORDER BY execution_order` or explicit `ORDER BY path` for deterministic results

---

## AI Integration & Observability

### Why pgmi is AI-Friendly

**Stable Contract:**
- Single-session model with deterministic inputs
- Plan-based execution (inspectable, predictable)
- Consistent CLI interface and exit codes
- Pure PostgreSQL (no proprietary DSL)

**Observability:**
- Real-time progress via `RAISE NOTICE`
- Diagnostic output via `RAISE DEBUG` (visible with `--verbose`, which sets `client_min_messages = 'debug'`)
- All output from SQL scripts (minimal pgmi abstraction)
- Errors propagate immediately with context
- Exit code: 0 = success, non-zero = failure

**Determinism:**
- Same inputs (files + params + connection) → same result
- No hidden state or implicit behavior
- Transaction semantics = PostgreSQL's guarantees

**Example: Agent-Friendly Output**
```
$ pgmi deploy ./myapp -d mydb --param env=production

NOTICE: Initializing deployment for myapp (env=production)
NOTICE: Executing: ./init.sql
NOTICE: Created schema: utils
NOTICE: Created schema: internal
NOTICE: Created schema: core
NOTICE: Created schema: api
NOTICE: Executing: ./migrations/001_users.sql
NOTICE: Created table: users
NOTICE: Executing: ./migrations/002_posts.sql
NOTICE: Created table: posts
NOTICE: Running tests (3 found)
NOTICE: ✓ Test: test_users_schema
NOTICE: ✓ Test: test_posts_schema
NOTICE: ✓ Test: test_foreign_keys
NOTICE: Deployment completed successfully

$ echo $?
0
```

**For Autonomous Agents:**
- Clear success/failure signal (exit code)
- Progress tracking via notices
- Errors have context (file, line, message)
- Transactional rollback on failure (safe to retry)

---

## Implementation Reference

### Key Interfaces (pkg/pgmi/)

```go
// Deployer: Main deployment service interface
type Deployer interface {
    Deploy(ctx context.Context, config DeploymentConfig) error
}

// DeploymentConfig: Deployment parameters
type DeploymentConfig struct {
    SourcePath          string            // Root directory containing deploy.sql
    DatabaseName        string            // Target database name
    MaintenanceDatabase string            // Database for server-level operations
    ConnectionString    string            // PostgreSQL connection string
    Overwrite           bool              // Enable drop/recreate workflow
    Force               bool              // Bypass interactive approval
    Parameters          map[string]string // Key-value pairs for pgmi_params
    Timeout             time.Duration     // Global deployment timeout
    Verbose             bool              // Enable detailed logging
    AuthMethod          AuthMethod        // Authentication mechanism
}
```

### Key Implementation Files

- `internal/services/deployer.go` - Main deployer implementation
- `internal/services/session.go` - Session preparation (temp tables, helpers)
- `internal/files/scanner/` - File discovery and checksumming
- `internal/files/loader/` - File loading into session tables
- `internal/db/` - Connection management and providers
- `pkg/pgmi/deployer.go` - Deployer interface
- `pkg/pgmi/connector.go` - Connector interface
- `pkg/pgmi/approver.go` - Approver interface
- `pkg/pgmi/types.go` - DeploymentConfig, TestConfig, etc.

---

## Troubleshooting

### Common Issues

**1. "deploy.sql not found"**
**Cause:** Required file missing
**Solution:** Every pgmi project must have `deploy.sql` at root

**2. "File not found"**
**Cause:** Query returns no rows for path
**Solution:**
- Check path matches discovered files (use `SELECT path FROM pg_temp.pgmi_source_view`)
- Ensure file not in `__test__/` (test files are in `pgmi_test_source_view`, not `pgmi_source_view`)

**3. "Required parameter not provided"**
**Cause:** A parameter declared with `p_required => true` was not supplied via `--param`
**Solution:** Provide the required parameter via CLI: `pgmi deploy . --param key=value`

**4. "Nothing executed"**
**Cause:** deploy.sql didn't query `pgmi_plan_view` or call `EXECUTE`
**Solution:** Ensure your deploy.sql loops over files and uses `EXECUTE v_file.content`

**5. Deployment fails with unclear error**
**Cause:** Default PostgreSQL output only shows NOTICE-level and above
**Solution:** Re-run with `--verbose` (`-v`) to enable `RAISE DEBUG` messages:
```bash
pgmi deploy ./myapp -d mydb --verbose
```
This sets `client_min_messages = 'debug'` on the session, surfacing all diagnostic output from SQL scripts.

---

## Quick Reference

### Session Objects

pgmi uses a two-tier API: internal tables (`_pgmi_*` prefix) and public views (`*_view` suffix).

| Object | Type | Purpose |
|--------|------|---------|
| `pg_temp._pgmi_source` | TABLE | All files except `deploy.sql` and tests |
| `pg_temp.pgmi_source_view` | VIEW | Public access to source files |
| `pg_temp.pgmi_plan_view` | VIEW | Files ordered by metadata for execution |
| `pg_temp._pgmi_test_source` | TABLE | Files from `__test__/` directories |
| `pg_temp.pgmi_test_source_view` | VIEW | Public access to test files |
| `pg_temp._pgmi_parameter` | TABLE | CLI parameters from `--param` |
| `pg_temp.pgmi_parameter_view` | VIEW | Public access to parameters |
| `pg_temp._pgmi_source_metadata` | TABLE | Parsed `<pgmi-meta>` blocks |

### Helper Functions

| Function | Purpose |
|----------|---------|
| `pgmi_test_plan(pattern)` | Returns test execution plan (TABLE function) |
| `pgmi_has_tests(directory, pattern)` | Check if tests exist |

### Parameter Access

Parameters are accessed via PostgreSQL's native `current_setting()`:

| Pattern | Purpose |
|---------|---------|
| `current_setting('pgmi.key')` | Access required parameter (fails if not set) |
| `COALESCE(current_setting('pgmi.key', true), 'default')` | Access optional parameter with default |

### Preprocessor Macro

| Macro | Purpose |
|-------|---------|
| `CALL pgmi_test()` or `CALL pgmi_test('./pattern/**')` | Run tests with automatic savepoints (expanded by Go) |
| `CALL pgmi_test('pattern', 'callback')` | Run tests with custom callback function |

### Checksum Types

| Type | Purpose | Changes Detected |
|------|---------|------------------|
| **Raw Checksum** | Exact content hash | Whitespace, comments, everything |
| **Normalized Checksum** | Content identity | Only functional changes (ignores formatting) |

### Transaction Patterns

| Pattern | Atomicity | Lock Duration | Use Case |
|---------|-----------|---------------|----------|
| **Single Transaction** | Maximum | Longest | Small deployments, low traffic |
| **Phased Commits** | Per-phase | Limited per phase | Long deployments, high traffic |
| **Savepoints** | Sub-transaction | Short | Optional features, error recovery |

---

## See Also

- **pgmi-sql skill:** Dollar-quoting, planning functions, testing patterns
- **pgmi-templates skill:** Metadata-driven deployment, advanced orchestration
- **pgmi-connections skill:** Connection factory, authentication providers
- **CLAUDE.md:** Core philosophy, CLI design, parameter system overview

