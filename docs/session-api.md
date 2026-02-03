# pgmi Session API Reference

> **The "AHA moment":** pgmi doesn't execute your SQL—it loads your files into PostgreSQL session tables and lets your SQL decide what to do with them.

## How pgmi Actually Works

When you run `pgmi deploy ./myproject`, here's what happens:

```
┌─────────────────────────────────────────────────────────────────────────┐
│  1. CONNECT                                                              │
│     pgmi connects to PostgreSQL                                         │
└────────────────────────────────────────────────┬────────────────────────┘
                                                 │
                                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  2. PREPARE SESSION                                                      │
│     pgmi creates temporary tables and loads your project files:          │
│                                                                          │
│     pg_temp.pgmi_source      ← All your .sql, .json, .yaml files        │
│     pg_temp.pgmi_parameter   ← CLI parameters (--param key=value)        │
│     pg_temp.pgmi_plan        ← Empty execution queue                     │
│     pg_temp.pgmi_plan_view   ← Pre-computed execution plan view          │
│                                                                          │
│     If --verbose: SET client_min_messages = 'debug' (enables RAISE DEBUG) │
│     Also creates helper functions: pgmi_plan_command(), pgmi_plan_file() │
└────────────────────────────────────────────────┬────────────────────────┘
                                                 │
                                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  3. EXECUTE deploy.sql (PLANNING PHASE)                                  │
│     YOUR deploy.sql runs and populates pg_temp.pgmi_plan                 │
│     by calling helper functions like pgmi_plan_file()                    │
│                                                                          │
│     ⚠️  These functions DON'T execute SQL—they INSERT into pgmi_plan    │
└────────────────────────────────────────────────┬────────────────────────┘
                                                 │
                                                 ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  4. EXECUTE THE PLAN (EXECUTION PHASE)                                   │
│     pgmi reads pg_temp.pgmi_plan in order and executes each command      │
│                                                                          │
│     ordinal=1: EXECUTE "BEGIN;"                                          │
│     ordinal=2: EXECUTE "CREATE TABLE users (...);"                       │
│     ordinal=3: EXECUTE "COMMIT;"                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

**The key insight:** deploy.sql is a *planning script*, not a deployment script. It builds a queue of commands. pgmi then executes that queue.

---

## Session Tables

These tables exist only for the duration of your deployment session. They're created in the `pg_temp` schema (PostgreSQL's session-local temporary namespace).

### pg_temp.pgmi_source

**Your entire project, in a table.**

Every file in your project directory is loaded here with full metadata:

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
| `is_sql_file` | boolean | True for `.sql`, `.ddl`, `.pgsql`, etc. |
| `parent_folder_name` | text | Immediate parent directory name |

**Example queries in deploy.sql:**

```sql
-- Find all migrations
SELECT path, content
FROM pg_temp.pgmi_source
WHERE directory = './migrations/' AND is_sql_file
ORDER BY path;

-- Find SQL files in a specific directory
SELECT path FROM pg_temp.pgmi_source
WHERE directory = './schemas' AND is_sql_file;

-- Count files by directory
SELECT directory, count(*)
FROM pg_temp.pgmi_source
GROUP BY directory;

-- Dynamic deployment based on environment
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path FROM pg_temp.pgmi_source
        WHERE directory LIKE './seeds/' || current_setting('pgmi.env', true) || '/%'
    ) LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;
END $$;
```

### pg_temp.pgmi_parameter

**CLI parameters as a queryable table.**

Parameters from `--param key=value` are stored here and also set as session variables:

| Column | Type | Description |
|--------|------|-------------|
| `key` | text | Parameter name (alphanumeric + underscores) |
| `value` | text | Parameter value (always text, cast as needed) |
| `type` | text | Declared type (`text`, `int`, `boolean`, `uuid`, etc.) |
| `required` | boolean | Whether parameter is required |
| `default_value` | text | Default if not provided |
| `description` | text | Human-readable description |

**Accessing parameters:**

```sql
-- Method 1: Direct session variable (fastest)
SELECT current_setting('pgmi.env');           -- Returns value or error
SELECT current_setting('pgmi.env', true);     -- Returns value or NULL

-- Method 2: Convenience wrapper with default
SELECT pg_temp.pgmi_get_param('env', 'development');

-- Method 3: Query the table (for introspection)
SELECT * FROM pg_temp.pgmi_parameter;
```

### pg_temp.pgmi_plan

**The execution queue your deploy.sql builds.**

| Column | Type | Description |
|--------|------|-------------|
| `ordinal` | integer | Auto-incrementing execution order |
| `command_sql` | text | SQL to execute |

This table starts empty. Your deploy.sql populates it by calling helper functions. After deploy.sql completes, pgmi reads this table in `ordinal` order and executes each command.

### pg_temp.pgmi_plan_view

**Pre-computed execution plan with metadata.**

This view joins `pgmi_source` with `pgmi_source_metadata` and provides:

| Column | Type | Description |
|--------|------|-------------|
| `path` | text | File path |
| `content` | text | File content |
| `checksum` | text | Normalized checksum |
| `generic_id` | uuid | Auto-generated UUID from path |
| `id` | uuid | Explicit ID from `<pgmi-meta>` (NULL if none) |
| `idempotent` | boolean | Whether file can be re-executed |
| `description` | text | From `<pgmi-meta>` |
| `sort_key` | text | Execution ordering key |
| `execution_order` | bigint | Sequential execution number |

**Use this view for metadata-driven deployment:**

```sql
-- Deploy only files with explicit metadata
SELECT path, id, sort_key
FROM pg_temp.pgmi_plan_view
WHERE id IS NOT NULL
ORDER BY execution_order;
```

### pg_temp.pgmi_unittest_plan

**Test execution plan with setup/teardown lifecycle.**

Files from `__test__/` directories are automatically moved here:

| Column | Type | Description |
|--------|------|-------------|
| `execution_order` | integer | Sequential order |
| `step_type` | text | `setup`, `test`, or `teardown` |
| `script_path` | text | Path to test file |
| `script_directory` | text | Directory containing test |
| `savepoint_id` | text | Unique savepoint name |
| `executable_sql` | text | Ready-to-execute SQL with savepoints |

---

## Helper Functions

All helper functions are created in `pg_temp` schema and available immediately in deploy.sql.

### pgmi_plan_command(sql text)

**Adds raw SQL to the execution plan.**

```sql
-- Transaction control
PERFORM pg_temp.pgmi_plan_command('BEGIN;');
PERFORM pg_temp.pgmi_plan_command('COMMIT;');

-- DDL statements
PERFORM pg_temp.pgmi_plan_command('CREATE SCHEMA IF NOT EXISTS app;');

-- Any valid SQL
PERFORM pg_temp.pgmi_plan_command($sql$
    CREATE TABLE users (
        id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
        email text UNIQUE NOT NULL
    );
$sql$);
```

### pgmi_plan_file(path text)

**Adds a file's content to the execution plan.**

```sql
-- Execute a specific file
PERFORM pg_temp.pgmi_plan_file('./migrations/001_init.sql');

-- Execute files in a loop
FOR v_file IN (
    SELECT path FROM pg_temp.pgmi_source
    WHERE directory = './schemas/'
    ORDER BY path
) LOOP
    PERFORM pg_temp.pgmi_plan_file(v_file.path);
END LOOP;
```

**Error handling:** Raises exception if file not found in `pgmi_source`.

### pgmi_plan_do(plpgsql_code text, VARIADIC args text[])

**Adds a PL/pgSQL anonymous block to the execution plan.**

```sql
-- Simple block
PERFORM pg_temp.pgmi_plan_do($$
BEGIN
    RAISE NOTICE 'Deployment starting...';
END;
$$);

-- With format() placeholders (%s = string, %I = identifier, %L = literal)
PERFORM pg_temp.pgmi_plan_do(
    $$BEGIN RAISE NOTICE 'Deploying to: %s'; END;$$,
    current_setting('pgmi.env')
);

-- Dynamic DDL
PERFORM pg_temp.pgmi_plan_do(
    $$BEGIN EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I', %L); END;$$,
    pg_temp.pgmi_get_param('app_schema', 'app')
);
```

### pgmi_plan_notice(message text, VARIADIC args text[])

**Adds a RAISE NOTICE to the execution plan (simpler than pgmi_plan_do).**

```sql
-- Simple message
PERFORM pg_temp.pgmi_plan_notice('Starting phase 1...');

-- With formatting
PERFORM pg_temp.pgmi_plan_notice('Executing: %s', v_file.path);
PERFORM pg_temp.pgmi_plan_notice('Environment: %s, Version: %s',
    current_setting('pgmi.env'),
    current_setting('pgmi.version', true)
);
```

### pgmi_plan_tests(pattern text DEFAULT '.*')

**Adds test execution to the plan with automatic setup/teardown.**

```sql
-- Run all tests
PERFORM pg_temp.pgmi_plan_tests();

-- Run tests matching a pattern (POSIX regex)
PERFORM pg_temp.pgmi_plan_tests('.*/integration/.*');
PERFORM pg_temp.pgmi_plan_tests('.*_critical\.sql$');

-- Run tests for a specific feature
PERFORM pg_temp.pgmi_plan_tests('.*/auth/.*');
```

**Automatic behavior:**
- Creates SAVEPOINTs before each `_setup.sql`
- Executes tests in order
- Rolls back to SAVEPOINT after tests (no side effects)
- Includes ancestor `_setup.sql` files needed by matching tests

### pgmi_declare_param(...)

**Declares a parameter with type validation, defaults, and documentation.**

```sql
SELECT pg_temp.pgmi_declare_param(
    p_key => 'env',
    p_type => 'text',
    p_default_value => 'development',
    p_required => false,
    p_description => 'Deployment environment'
);

SELECT pg_temp.pgmi_declare_param(
    p_key => 'max_connections',
    p_type => 'int',
    p_default_value => '100'
);

SELECT pg_temp.pgmi_declare_param(
    p_key => 'admin_password',
    p_type => 'text',
    p_required => true  -- Fails if not provided
);
```

**Supported types:** `text`, `int`, `integer`, `bigint`, `numeric`, `boolean`, `bool`, `uuid`, `timestamp`, `timestamptz`, `name`

### pgmi_get_param(key text, default text)

**Gets a parameter value with fallback.**

```sql
-- With default
SELECT pg_temp.pgmi_get_param('env', 'development');

-- In conditional logic
IF pg_temp.pgmi_get_param('env', 'dev') = 'production' THEN
    -- Production-specific logic
END IF;
```

---

## The Two-Phase Model (Critical Concept)

**This is the most important thing to understand about pgmi.**

```
deploy.sql runs          pgmi reads pgmi_plan
      │                         │
      ▼                         ▼
┌─────────────┐           ┌─────────────┐
│   PHASE 1   │           │   PHASE 2   │
│  PLANNING   │           │  EXECUTION  │
│             │           │             │
│ Calls to    │           │ Actually    │
│ pgmi_plan_* │──────────▶│ runs the    │
│ INSERT into │           │ SQL         │
│ pgmi_plan   │           │             │
└─────────────┘           └─────────────┘
```

### The Common Mistake

```sql
-- ❌ WRONG: This doesn't work as expected
BEGIN;  -- This runs NOW, during planning
FOR v_file IN (SELECT path FROM pg_temp.pgmi_source) LOOP
    PERFORM pg_temp.pgmi_plan_file(v_file.path);  -- This just INSERTs a row
END LOOP;
COMMIT;  -- This commits the INSERTs, not your deployment
```

**What happened:** You wrapped the *planning* in a transaction, not the *execution*.

### The Correct Pattern

```sql
-- ✅ CORRECT: Schedule BEGIN/COMMIT as commands to be executed
PERFORM pg_temp.pgmi_plan_command('BEGIN;');
FOR v_file IN (SELECT path FROM pg_temp.pgmi_source) LOOP
    PERFORM pg_temp.pgmi_plan_file(v_file.path);
END LOOP;
PERFORM pg_temp.pgmi_plan_command('COMMIT;');
```

**What happens:**
1. deploy.sql runs, populating pgmi_plan with: `BEGIN;`, file1, file2, ..., `COMMIT;`
2. pgmi reads pgmi_plan and executes: `BEGIN;` → file1 → file2 → ... → `COMMIT;`

---

## Common Patterns

### Phased Deployment

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- Phase 1: Schema changes
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    PERFORM pg_temp.pgmi_plan_notice('=== Phase 1: Schema ===');

    FOR v_file IN (
        SELECT path FROM pg_temp.pgmi_source
        WHERE directory = './schemas/' AND is_sql_file
        ORDER BY path
    ) LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');

    -- Phase 2: Migrations
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    PERFORM pg_temp.pgmi_plan_notice('=== Phase 2: Migrations ===');

    FOR v_file IN (
        SELECT path FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
```

### Conditional Deployment

```sql
DO $$
BEGIN
    IF pg_temp.pgmi_get_param('env') = 'development' THEN
        PERFORM pg_temp.pgmi_plan_file('./seeds/dev_data.sql');
    END IF;

    IF pg_temp.pgmi_get_param('include_tests', 'true')::boolean THEN
        PERFORM pg_temp.pgmi_plan_tests();
    END IF;
END $$;
```

### Dynamic File Selection

```sql
-- Deploy SQL files from a specific directory (preferred)
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path FROM pg_temp.pgmi_source
        WHERE directory = './migrations/v2' AND is_sql_file
        ORDER BY path
    ) LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;
END $$;

-- Or use POSIX regex for complex patterns (e.g., files in any 'v2' subdirectory)
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path FROM pg_temp.pgmi_source
        WHERE path ~ '.*/v2/.*' AND is_sql_file  -- Combine regex with is_sql_file
        ORDER BY path
    ) LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;
END $$;
```

### Test Isolation with Savepoints

```sql
PERFORM pg_temp.pgmi_plan_command('BEGIN;');

-- Deploy your schema
FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './schemas/') LOOP
    PERFORM pg_temp.pgmi_plan_file(v_file.path);
END LOOP;

-- Run tests in a savepoint (rolled back after)
PERFORM pg_temp.pgmi_plan_command('SAVEPOINT before_tests;');
PERFORM pg_temp.pgmi_plan_tests();
PERFORM pg_temp.pgmi_plan_command('ROLLBACK TO SAVEPOINT before_tests;');

-- Tests passed, commit the real changes
PERFORM pg_temp.pgmi_plan_command('COMMIT;');
```

---

## Introspection Examples

### See What Files Are Loaded

```sql
-- In deploy.sql or via psql
SELECT path, size_bytes, is_sql_file
FROM pg_temp.pgmi_source
ORDER BY path;
```

### See What Parameters Are Available

```sql
SELECT key, value, type, required, description
FROM pg_temp.pgmi_parameter;
```

### Preview the Execution Plan

```sql
-- After your planning logic runs
SELECT ordinal, left(command_sql, 80) AS preview
FROM pg_temp.pgmi_plan
ORDER BY ordinal;
```

### See Available Tests

```sql
SELECT step_type, script_path
FROM pg_temp.pgmi_unittest_plan
ORDER BY execution_order;
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
4. Execute what your deploy.sql planned

**Your job is to:**
1. Query `pgmi_source` to find your files
2. Use `pgmi_plan_*` functions to build an execution plan
3. Control transaction boundaries with `BEGIN`/`COMMIT`
4. Decide which files run in what order

**The result:** Complete control. No magic. PostgreSQL is the deployment engine—pgmi just provides infrastructure.
