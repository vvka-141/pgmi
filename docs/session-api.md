# pgmi Session API Reference

> **The "AHA moment":** pgmi doesn't execute your SQL—it loads your files into PostgreSQL session tables and lets your SQL decide what to do with them.

## How pgmi Actually Works

When you run `pgmi deploy ./myproject`, here's what happens:

```
┌─────────────────────────────────────────────────────────────────────────┐
│  1. CONNECT                                                              │
│     pgmi connects to PostgreSQL                                         │
└────────────────────────────────────────────┬────────────────────────────┘
                                             │
                                             ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  2. PREPARE SESSION                                                      │
│     pgmi creates temporary tables and views (two-tier API):              │
│                                                                          │
│     Internal tables: _pgmi_source, _pgmi_parameter, _pgmi_test_source    │
│     Public views:    pgmi_source_view, pgmi_parameter_view, pgmi_plan_view│
│                      pgmi_test_source_view, pgmi_test_directory_view     │
│                                                                          │
│     If --verbose: SET client_min_messages = 'debug' (enables RAISE DEBUG)│
│     Functions: pgmi_test_plan(), pgmi_test_generate()                    │
└────────────────────────────────────────────┬────────────────────────────┘
                                             │
                                             ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  3. EXECUTE deploy.sql                                                   │
│     YOUR deploy.sql runs and directly executes files using:              │
│                                                                          │
│     FOR v_file IN (SELECT * FROM pgmi_plan_view ORDER BY execution_order)│
│     LOOP                                                                 │
│         EXECUTE v_file.content;                                          │
│     END LOOP;                                                            │
│                                                                          │
│     Transaction boundaries, error handling, execution order—all yours.   │
└─────────────────────────────────────────────────────────────────────────┘
```

**The key insight:** deploy.sql is the deployment script. It queries `pgmi_plan_view` and uses `EXECUTE` to run files directly. You control the deployment logic — transactions, ordering, conditionals, error handling.

**Connection requirement:** Because everything depends on `pg_temp` tables surviving for the entire session, pgmi requires a direct connection or a pooler in session mode. Transaction-mode poolers (PgBouncer, RDS Proxy, etc.) will silently break deployments by reassigning the backend connection. See [Production Guide — Connection Requirements](PRODUCTION.md#connection-requirements).

---

## Two-Tier API Design

pgmi uses a two-tier naming convention for session objects:

| Tier | Naming | Purpose | Example |
|------|--------|---------|---------|
| **Internal** | `_pgmi_*` prefix | Used by pgmi Go code | `_pgmi_source`, `_pgmi_parameter` |
| **Public** | `*_view` suffix | Stable API for deploy.sql | `pgmi_source_view`, `pgmi_plan_view` |

**Why this matters:**
- Internal tables may change between versions
- Public views provide a stable contract for deploy.sql
- Always use views (`pgmi_source_view`, `pgmi_plan_view`) in your SQL

---

## Public Interface (Stable API)

These views and functions are the stable API for deploy.sql. Use these instead of querying internal tables directly.

### Which View Should I Use?

| Use Case | View | Why |
|----------|------|-----|
| **Deploying files** | `pgmi_plan_view` | Pre-sorted by execution order, includes metadata |
| **Introspection/debugging** | `pgmi_source_view` | Raw file access, all columns available |
| **Custom ordering** | `pgmi_source_view` | Apply your own `ORDER BY` logic |
| **Metadata-driven deployment** | `pgmi_plan_view` | Respects `<pgmi-meta>` sort keys |

**Rule of thumb:** Use `pgmi_plan_view` for deployment loops. Use `pgmi_source_view` when you need raw access or custom filtering beyond what the plan provides.

### File Access

#### pgmi_source_view

**All project source files (excludes deploy.sql and `__test__/` files).**

This view provides direct access to all discovered files. For most use cases, prefer `pgmi_plan_view` which adds execution ordering via metadata.

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | Normalized path (always starts with `./`) |
| `name` | text | Filename without directory |
| `directory` | text | Directory path ending with `/` |
| `extension` | text | File extension (e.g., `.sql`) |
| `depth` | integer | Nesting level (0 = root) |
| `content` | text | Full file content |
| `size_bytes` | bigint | File size in bytes |
| `checksum` | text | SHA-256 of original content |
| `pgmi_checksum` | text | SHA-256 of normalized content |
| `path_parts` | text[] | Path split by `/` |
| `is_sql_file` | boolean | True for recognized SQL extensions |
| `is_test_file` | boolean | Always `false` in this view — files matching `__test__/` are routed to `pgmi_test_source_view` instead |
| `parent_folder_name` | text | Immediate parent directory name |

```sql
-- List all SQL files in migrations/
SELECT path, name FROM pg_temp.pgmi_source_view
WHERE directory = './migrations/' AND is_sql_file
ORDER BY path;
```

#### pgmi_plan_view

**Pre-computed execution plan with metadata.**

This view joins `_pgmi_source` with `_pgmi_source_metadata` and provides a clean interface for file access:

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | File path |
| `content` | text | File content |
| `checksum` | text | Normalized checksum |
| `generic_id` | uuid | Auto-generated UUID from path |
| `id` | uuid | Explicit ID from [`<pgmi-meta>`](METADATA.md) (NULL if none) |
| `idempotent` | boolean | Whether file can be re-executed (defaults to `true` for files without metadata) |
| `description` | text | From `<pgmi-meta>` (defaults to `''` for files without metadata, never NULL) |
| `sort_key` | text | Execution ordering key |
| `execution_order` | bigint | Sequential execution number |

**Recommended usage:**

```sql
-- Deploy files in metadata-driven order using direct execution
DO $$
DECLARE v_file RECORD;
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

### Parameters

CLI parameters (passed via `--param key=value`) are accessible in multiple ways. This section consolidates all parameter access patterns.

#### Method 1: Session Variables (Recommended)

pgmi automatically sets session variables with the `pgmi.` prefix. This is the simplest and most common approach:

```sql
-- Get parameter with default (the true argument prevents errors if not set)
v_env := COALESCE(current_setting('pgmi.env', true), 'development');

-- In conditional logic
IF COALESCE(current_setting('pgmi.env', true), 'dev') = 'production' THEN
    -- Production-specific logic
END IF;

-- Check if parameter was provided
IF current_setting('pgmi.feature_flag', true) IS NOT NULL THEN
    -- Parameter was explicitly set
END IF;
```

**Important:** Always pass `true` as the second argument to `current_setting()`. This returns NULL instead of raising an error when the variable is not set.

#### Method 2: pgmi_parameter_view (Introspection)

For iterating over parameters or building dynamic logic:

| Column | Type | Description |
|--------|------|-------------|
| `key` | text | Parameter name (e.g., `env`, `version`) |
| `value` | text | Parameter value (always text, cast as needed) |
| `type` | text | Declared type hint (`text`, `int`, `boolean`, etc.) |
| `required` | boolean | Whether parameter was marked required |
| `default_value` | text | Default value if not provided |
| `description` | text | Human-readable description |

```sql
-- List all parameters
SELECT key, value, description FROM pg_temp.pgmi_parameter_view;

-- Iterate over parameters dynamically
DO $$
DECLARE
    v_param RECORD;
BEGIN
    FOR v_param IN SELECT key, value FROM pg_temp.pgmi_parameter_view LOOP
        RAISE NOTICE 'Parameter: % = %', v_param.key, v_param.value;
    END LOOP;
END $$;
```

#### Method 3: deployment_setting() Helper (Advanced Template Only)

The advanced template provides a helper function with error handling:

```sql
-- Get required parameter (raises exception if missing)
v_admin_role := pg_temp.deployment_setting('database_admin_role');

-- Get optional parameter (returns NULL if missing)
v_optional := pg_temp.deployment_setting('optional_key', false);
```

**Note:** This function uses a `deployment.` prefix internally and normalizes key names. It's defined in the advanced template's `deploy.sql`, not in pgmi core.

#### Parameter Precedence

Parameters merge from multiple sources (later wins):

```
pgmi.yaml params < --params-file < --param CLI flag
```

See [Configuration Reference](CONFIGURATION.md) for details.

#### Type Coercion

All parameter values are stored as text. Cast them as needed:

```sql
-- Boolean
v_enabled := COALESCE(current_setting('pgmi.feature_enabled', true), 'false')::boolean;

-- Integer
v_limit := COALESCE(current_setting('pgmi.max_rows', true), '100')::int;

-- Timestamp
v_cutoff := COALESCE(current_setting('pgmi.since', true), '2024-01-01')::timestamp;
```

#### Template Responsibility

pgmi core provides raw parameter storage. Templates handle:
- Declaring expected parameters (advanced template uses `session.xml`)
- Validating required parameters
- Providing default values
- Type validation and coercion

### Direct Execution Pattern

pgmi uses a **direct execution** model: your deploy.sql queries `pgmi_plan_view` and uses `EXECUTE` to run files. This gives you full control over transaction boundaries, execution order, and conditional logic.

**Basic pattern:**

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- Transaction control is in your hands
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './schemas/%'
        ORDER BY execution_order
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

**With explicit transaction boundaries:**

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- Phase 1: Schema changes in one transaction
    BEGIN
        FOR v_file IN (
            SELECT path, content FROM pg_temp.pgmi_plan_view
            WHERE path LIKE './schemas/%' ORDER BY execution_order
        ) LOOP
            EXECUTE v_file.content;
        END LOOP;
    EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION 'Schema phase failed: %', SQLERRM;
    END;

    -- Phase 2: Migrations
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%' ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

**Conditional execution:**

```sql
DO $$
DECLARE
    v_file RECORD;
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%' ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

    -- Only seed data in development
    IF v_env = 'development' THEN
        FOR v_file IN (
            SELECT path, content FROM pg_temp.pgmi_plan_view
            WHERE path LIKE './seeds/%' ORDER BY execution_order
        ) LOOP
            EXECUTE v_file.content;
        END LOOP;
    END IF;
END $$;
```

### Metadata

#### pgmi_source_metadata_view

**Parsed `<pgmi-meta>` XML blocks from SQL files.**

Files without metadata are not in this view (use `pgmi_plan_view` which handles fallbacks).

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | File path (references `pgmi_source_view.path`) |
| `id` | uuid | Explicit script UUID from metadata |
| `idempotent` | boolean | Whether script can be re-executed safely |
| `sort_keys` | text[] | Array of execution ordering keys |
| `description` | text | Human-readable description |

```sql
-- List files with metadata
SELECT path, id, idempotent, sort_keys
FROM pg_temp.pgmi_source_metadata_view;

-- Find non-idempotent migrations
SELECT path FROM pg_temp.pgmi_source_metadata_view
WHERE NOT idempotent;
```

See [Metadata Guide](METADATA.md) for syntax and usage patterns.

### Test Views

#### pgmi_test_source_view

**Test file content from `__test__/` or `__tests__/` directories.**

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | Full path to test file |
| `directory` | text | Parent test directory (ending with `/`) |
| `filename` | text | Filename without directory |
| `content` | text | Full file content |
| `is_fixture` | boolean | True for `_setup.sql` files |

```sql
-- List all test files
SELECT path, is_fixture FROM pg_temp.pgmi_test_source_view
ORDER BY directory, filename;

-- Get fixture files only
SELECT path FROM pg_temp.pgmi_test_source_view
WHERE is_fixture;
```

#### pgmi_test_directory_view

**Hierarchical test directory structure.**

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | Directory path (ending with `/`) |
| `parent_path` | text | Parent directory path (NULL for root) |
| `depth` | integer | Nesting level (0 = root `__test__/`) |

```sql
-- See test directory hierarchy
SELECT path, parent_path, depth
FROM pg_temp.pgmi_test_directory_view
ORDER BY depth, path;

-- Find nested test directories
SELECT path FROM pg_temp.pgmi_test_directory_view
WHERE depth > 0;
```

### Testing

#### CALL pgmi_test() Preprocessor Macro

**Executes tests with automatic savepoint isolation.**

The `CALL pgmi_test()` is a **preprocessor macro** that Go expands before sending SQL to PostgreSQL:

```sql
-- Run all tests with default callback
CALL pgmi_test();

-- Run tests matching a pattern (POSIX regex)
CALL pgmi_test('.*/integration/.*');
CALL pgmi_test('.*_critical\.sql$');

-- Run tests with custom callback function
CALL pgmi_test('.*/auth/.*', 'my_custom_callback');
```

**Automatic behavior:**
- Creates SAVEPOINTs before each `_setup.sql`
- Executes tests in lexicographic order
- Rolls back to SAVEPOINT after tests (no side effects)
- Includes ancestor `_setup.sql` files needed by matching tests
- Calls `pgmi_test_generate()` internally to produce inline SQL

#### pgmi_test_plan(pattern) Function

**Returns the test execution plan as a table (for introspection).**

This is a TABLE-returning function (not a view). Files from `__test__/` or `__tests__/` directories are automatically organized into a depth-first execution plan with fixture/test/teardown lifecycle.

| Column | Type | Description |
|--------|------|-------------|
| `ordinal` | integer | Sequential execution order (1-based) |
| `step_type` | text | `'fixture'`, `'test'`, or `'teardown'` |
| `script_path` | text | Path to test file (NULL for teardown) |
| `directory` | text | Test directory containing the script |
| `depth` | integer | Nesting level (0 = root `__test__/`) |

```sql
-- See what tests would run
SELECT * FROM pg_temp.pgmi_test_plan();

-- Filter by pattern (POSIX regex on script_path)
SELECT * FROM pg_temp.pgmi_test_plan('.*/auth/.*');
```

**Test execution emits notices:**
- `NOTICE: [pgmi] Fixture: ./path/to/_setup.sql`
- `NOTICE: [pgmi] Test: ./path/to/test_example.sql`

With `--verbose`, DEBUG messages show rollback and teardown events (`[pgmi] Rollback: ...`, `[pgmi] Teardown: ...`).

#### pgmi_test_generate(pattern, callback) Function

**Generates the SQL code for `pgmi_test()` macro expansion.**

This is an internal function called by the Go preprocessor. It returns the complete SQL text that replaces the `CALL pgmi_test()` macro.

```sql
-- See what SQL the macro generates (for debugging)
SELECT pg_temp.pgmi_test_generate();
SELECT pg_temp.pgmi_test_generate('.*/auth/.*', 'my_callback');
```

**Critical implementation detail:** The generated SQL uses **top-level SAVEPOINT commands**, not PL/pgSQL savepoints. PostgreSQL's PL/pgSQL does not support `SAVEPOINT`, `ROLLBACK TO SAVEPOINT`, or `RELEASE SAVEPOINT` commands directly — they must be issued as top-level SQL statements.

The generated structure looks like:
```sql
SAVEPOINT pgmi_fixture_1;           -- Top-level SQL
DO $$ ... EXECUTE fixture ... $$;   -- Test content via EXECUTE
SAVEPOINT pgmi_test_1;              -- Top-level SQL
DO $$ ... EXECUTE test ... $$;      -- Test content via EXECUTE
ROLLBACK TO SAVEPOINT pgmi_test_1;  -- Top-level SQL (undoes test)
ROLLBACK TO SAVEPOINT pgmi_fixture_1; -- Top-level SQL (undoes fixture)
```

This is why `CALL pgmi_test()` must appear at the top level of your deploy.sql, not inside a DO block.

#### pgmi_persist_test_plan(schema, pattern) Function

**Exports the test plan to a permanent table for external tooling.**

```sql
-- Create a permanent copy of the test plan
SELECT pg_temp.pgmi_persist_test_plan('public', NULL);
-- Creates: public.pgmi_test_plan
```

| Parameter | Type | Description |
|-----------|------|-------------|
| `schema` | text | Target schema for the snapshot table |
| `pattern` | text | Optional POSIX regex filter (NULL = all tests) |

This is useful for CI/CD pipelines that need to inspect the test plan before running, or for generating test reports.

---

## The Direct Execution Model (Critical Concept)

**This is the most important thing to understand about pgmi.**

```
pgmi prepares session    deploy.sql runs
      │                         │
      ▼                         ▼
┌─────────────┐           ┌─────────────┐
│   SETUP     │           │  EXECUTION  │
│             │           │             │
│ Create temp │           │ Query files │
│ tables with │──────────▶│ from views  │
│ your files  │           │ EXECUTE     │
│             │           │ directly    │
└─────────────┘           └─────────────┘
```

**Your deploy.sql has full control.** You query `pgmi_plan_view`, loop through files, and use `EXECUTE` to run them. Transaction boundaries, error handling, execution order—all in your hands.

### The Basic Pattern

```sql
DO $$
DECLARE v_file RECORD;
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

**What happens:**
1. pgmi loads your files into internal tables and creates public views
2. Your deploy.sql queries `pgmi_plan_view` (or `pgmi_source_view`) and executes files directly with `EXECUTE`

---

## Common Patterns

### Phased Deployment

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- Phase 1: Schema changes
    RAISE NOTICE '=== Phase 1: Schema ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './schemas/%'
        ORDER BY execution_order
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;

    -- Phase 2: Migrations
    RAISE NOTICE '=== Phase 2: Migrations ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### Conditional Deployment

`CALL pgmi_test()` is a preprocessor macro that expands to top-level SQL (including `SAVEPOINT` commands), so it **must** appear at the top level of deploy.sql — never inside a `DO` block. Structure your deploy.sql with the DO block for migrations and `CALL pgmi_test()` as a separate top-level statement:

```sql
-- Phase 1: Migrations and seeds (inside DO block)
DO $$
DECLARE
    v_file RECORD;
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
BEGIN
    -- Always run migrations
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

    -- Only seed data in development
    IF v_env = 'development' THEN
        FOR v_file IN (
            SELECT path, content FROM pg_temp.pgmi_plan_view
            WHERE path LIKE './seeds/%'
            ORDER BY execution_order
        ) LOOP
            EXECUTE v_file.content;
        END LOOP;
    END IF;
END $$;

-- Phase 2: Tests (top-level — expands to SAVEPOINT commands)
CALL pgmi_test();

COMMIT;
```

### Dynamic File Selection

```sql
-- Deploy SQL files from a specific directory
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/v2/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Or use POSIX regex for complex patterns
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE path ~ '.*/v2/.*' AND is_sql_file
        ORDER BY path
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### Test Isolation with Savepoints

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- Deploy your schema
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './schemas/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

END $$;

-- Run tests (preprocessor macro handles savepoint isolation)
CALL pgmi_test();

COMMIT;
```

---

## Internal Tables (Implementation Details)

These tables are the underlying storage for the session API. Users should use the public views above rather than querying these internal tables directly.

**Naming convention:** Internal tables use underscore prefix (`_pgmi_*`)

### pg_temp._pgmi_source

**Raw file storage.**

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | Normalized path (always starts with `./`) |
| `name` | text | Filename without directory |
| `directory` | text | Directory path ending with `/` |
| `extension` | text | File extension (e.g., `.sql`) |
| `depth` | integer | Nesting level (0 = root) |
| `content` | text | Full file content |
| `size_bytes` | bigint | File size |
| `checksum` | text | SHA-256 of original content |
| `pgmi_checksum` | text | SHA-256 of normalized content (for idempotency) |
| `path_parts` | text[] | Path split by `/` |
| `is_sql_file` | boolean | True for SQL file extensions |
| `is_test_file` | boolean | Always `false` — a CHECK constraint routes test files to `_pgmi_test_source` instead |
| `parent_folder_name` | text | Immediate parent directory name |

### pg_temp._pgmi_parameter

**Raw parameter storage.**

| Column | Type | Description |
|--------|------|-------------|
| `key` | text | Parameter name |
| `value` | text | Parameter value |
| `type` | text | Declared type hint |
| `required` | boolean | Whether parameter is required |
| `default_value` | text | Default if not provided |
| `description` | text | Human-readable description |

### pg_temp._pgmi_source_metadata

**Parsed XML metadata from `<pgmi-meta>` blocks.**

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | File path (FK to `_pgmi_source.path`) |
| `id` | uuid | Explicit script UUID |
| `idempotent` | boolean | Whether script can be re-executed |
| `sort_keys` | text[] | Execution ordering keys (defaults to `{}`) |
| `description` | text | Human-readable description |

### pg_temp._pgmi_test_directory

**Test directory hierarchy.**

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | Directory path (ending with `/`) |
| `parent_path` | text | Parent directory (NULL for root) |
| `depth` | integer | Nesting level |

### pg_temp._pgmi_test_source

**Test file content.**

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | Full file path |
| `directory` | text | Parent test directory (FK to `_pgmi_test_directory.path`) |
| `filename` | text | Filename only |
| `content` | text | Full file content |
| `is_fixture` | boolean | True for `_setup.sql` files |

---

## Introspection Examples

### See What Files Are Loaded

```sql
-- Use the view for clean access
SELECT path, execution_order, idempotent
FROM pg_temp.pgmi_plan_view
ORDER BY execution_order;
```

### See What Parameters Are Available

```sql
SELECT key, value, type, required, default_value, description
FROM pg_temp.pgmi_parameter_view;
```

### Preview the Execution Plan

```sql
-- See files in execution order
SELECT execution_order, path, left(content, 80) AS preview
FROM pg_temp.pgmi_plan_view
ORDER BY execution_order;
```

### See Available Tests

```sql
SELECT step_type, script_path
FROM pg_temp.pgmi_test_plan()
ORDER BY ordinal;
```

---

## Philosophy: Why This Design?

pgmi is not a migration framework. It's an **execution fabric**.

| Traditional Migration Tool | pgmi |
|---------------------------|------|
| Decides execution order | Your SQL queries and filters the plan |
| Controls transactions | Your SQL controls transactions |
| Provides retry logic | Your SQL provides retry logic (EXCEPTION blocks) |
| Has migration history table | You implement tracking however you want |
| Black box | Transparent — session state is queryable |

**pgmi's job is to:**
1. Connect to PostgreSQL
2. Load your files into session tables
3. Run your deploy.sql

**Your job is to:**
1. Query `pgmi_plan_view` to find your files
2. Use `EXECUTE` to run them directly
3. Control transaction boundaries and error handling
4. Decide which files run in what order

**The result:** Full control over deployment logic. pgmi handles infrastructure (file loading, metadata parsing, preprocessing); your SQL handles everything else. PostgreSQL is the deployment engine.

---

## See Also

- [Testing Guide](TESTING.md) — Database testing with automatic rollback
- [Metadata Guide](METADATA.md) — Script tracking and execution ordering
- [MCP Integration](MCP.md) — Model Context Protocol for AI assistants (advanced template)
