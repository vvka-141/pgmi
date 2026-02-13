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

**The key insight:** deploy.sql is the deployment script. It queries `pgmi_plan_view` and uses `EXECUTE` to run files directly. You control everything.

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

### File Access

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
| `idempotent` | boolean | Whether file can be re-executed |
| `description` | text | From `<pgmi-meta>` |
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

CLI parameters (passed via `--param key=value`) are accessible in two ways:

#### Session Variables (Recommended)

pgmi automatically sets session variables with the `pgmi.` prefix:

```sql
-- Get parameter with default
v_env := COALESCE(current_setting('pgmi.env', true), 'development');

-- In conditional logic
IF COALESCE(current_setting('pgmi.env', true), 'dev') = 'production' THEN
    -- Production-specific logic
END IF;
```

**Note:** The second argument `true` to `current_setting()` is important—it returns NULL instead of raising an error when the variable is not set.

#### pgmi_parameter_view

For introspection, you can query the raw parameters:

```sql
SELECT key, value FROM pg_temp.pgmi_parameter_view;
```

**Template responsibility:** Type validation, required parameter checking, and default values are handled by templates, not pgmi core. The advanced template provides its own `deployment_setting()` helper function. Simple templates can use `COALESCE(current_setting(...), 'default')` directly.

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

```sql
-- See what tests would run
SELECT * FROM pg_temp.pgmi_test_plan();

-- Filter by pattern
SELECT * FROM pg_temp.pgmi_test_plan('.*/auth/.*');
```

Returns: `ordinal`, `step_type` (fixture/test/teardown), `script_path`, `directory`, `depth`

#### pgmi_test_plan (function)

**Returns the test execution plan with fixture/teardown lifecycle.**

This is a TABLE-returning function (not a view). Files from `__test__/` or `__tests__/` directories are automatically organized:

| Column | Type | Description |
|--------|------|-------------|
| `ordinal` | integer | Sequential order |
| `step_type` | text | `fixture`, `test`, or `teardown` |
| `script_path` | text | Path to test file |
| `directory` | text | Directory containing test |
| `depth` | integer | Nesting level |

**Test execution emits notices:**
- `NOTICE: Fixture: ./path/to/_setup.sql`
- `NOTICE: Test: ./path/to/test_example.sql`
- `NOTICE: Teardown: ./path/to/__test__/`

With `--verbose`, DEBUG messages show savepoint operations.

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

```sql
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

    -- Optionally run tests
    IF COALESCE(current_setting('pgmi.include_tests', true), 'true')::boolean THEN
        CALL pgmi_test();
    END IF;
END $$;
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
| `parent_folder_name` | text | Immediate parent directory name |

### pg_temp._pgmi_parameter

**Raw parameter storage.**

| Column | Type | Description |
|--------|------|-------------|
| `key` | text | Parameter name |
| `value` | text | Parameter value |
| `type` | text | Declared type |
| `required` | boolean | Whether parameter is required |
| `default_value` | text | Default if not provided |
| `description` | text | Human-readable description |

### pg_temp._pgmi_source_metadata

**Parsed XML metadata from `<pgmi-meta>` blocks.**

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
SELECT key, value, type, required, description
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
| Decides execution order | Your SQL decides execution order |
| Controls transactions | Your SQL controls transactions |
| Provides retry logic | Your SQL provides retry logic (EXCEPTION blocks) |
| Has migration history table | You implement tracking however you want |
| Black box | Transparent—everything is queryable |

**pgmi's job is to:**
1. Connect to PostgreSQL
2. Load your files into session tables
3. Run your deploy.sql

**Your job is to:**
1. Query `pgmi_plan_view` to find your files
2. Use `EXECUTE` to run them directly
3. Control transaction boundaries and error handling
4. Decide which files run in what order

**The result:** Complete control. No magic. PostgreSQL is the deployment engine—pgmi just provides infrastructure.

---

## See Also

- [Testing Guide](TESTING.md) — Database testing with automatic rollback
- [Metadata Guide](METADATA.md) — Script tracking and execution ordering
- [MCP Integration](MCP.md) — Model Context Protocol for AI assistants (advanced template)
