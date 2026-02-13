---
name: pgmi-sql
description: "Use when writing SQL/PL/pgSQL or deploy.sql"
user_invocable: true
---

**Use this skill when:**
- Writing SQL or PL/pgSQL code for pgmi projects
- Creating functions, procedures, or schema definitions
- Writing deploy.sql orchestration logic
- Implementing tests in `__test__/` directories
- Debugging SQL syntax or logic issues

---

## Core Philosophy

**SQL-First Approach:**
- Prefer pure SQL over PL/pgSQL when possible
- Use PostgreSQL's native functionality (don't reinvent)
- Prioritize robustness, conciseness, and elegance
- When in doubt, test the simplest solution first

**Fail-Fast Testing:**
- Use `RAISE EXCEPTION` for test failures
- No custom assertion frameworks
- Tests succeed silently, fail loudly
- PostgreSQL's native error handling is enough

---

## Table Naming: Singular, Not Plural

**CRITICAL: Always use singular table names in PostgreSQL.**

### Why Singular?

In PostgreSQL, every table automatically creates a composite type with the same name. This type is used in function signatures:

```sql
-- ❌ BAD: Plural table name
CREATE TABLE users (...);
CREATE FUNCTION get_user() RETURNS users;  -- Awkward: "returns users"

-- ✅ GOOD: Singular table name
CREATE TABLE "user" (...);
CREATE FUNCTION get_user() RETURNS "user";  -- Natural: "returns user"
```

### The Type System Connection

```sql
-- Table creates implicit type
CREATE TABLE account (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL
);

-- Type used naturally in functions
CREATE FUNCTION create_account(p_email TEXT) RETURNS account AS $$
    INSERT INTO account (email) VALUES (p_email) RETURNING *;
$$ LANGUAGE SQL;

-- Variables use the type
DECLARE
    v_account account;  -- Reads naturally: "variable account"
BEGIN
    v_account := create_account('test@example.com');
```

### Reserved Words

When using reserved words like `user`, quote the identifier:

```sql
CREATE TABLE "user" (...);           -- Quoted to avoid conflict
SELECT * FROM "user" WHERE id = $1;  -- Must quote in all references
```

### Naming Conventions Summary

| Entity | Convention | Example |
|--------|------------|---------|
| Tables | Singular, snake_case | `account`, `"user"`, `http_request` |
| Functions | Verb + noun | `create_account`, `get_user`, `delete_order` |
| Views | Singular or descriptive | `account_summary`, `active_subscription` |
| Columns | snake_case | `created_at`, `email_verified` |

---

## Test File Naming: `_setup.sql`

**MANDATORY: Fixture files must be named `_setup.sql` (or `_setup.psql`).**

This is the strictest naming convention in pgmi. No alternatives.

```
__test__/
  _setup.sql           ✅ CORRECT: Exact name required
  test_user_crud.sql   ✅ CORRECT: Test file
  00_fixture.sql       ❌ WRONG: Not recognized as fixture
  setup.sql            ❌ WRONG: Missing underscore prefix
  fixture.sql          ❌ WRONG: Not the canonical name
```

The `_setup.sql` file:
- Runs ONCE before all tests in its directory
- Creates test fixtures (INSERT test data)
- Is automatically wrapped in SAVEPOINT for rollback
- Must exist if tests need shared fixture data

---

## Dollar-Quoting for String Literals

**Always use PostgreSQL dollar-quoting (`$$`) syntax for multi-line string literals and embedded SQL code.**

### Why Dollar-Quoting?

1. **Better IDE Support** - Syntax highlighting works correctly for embedded SQL/PL/pgSQL blocks
2. **No Escaping Needed** - Eliminates complex quote escaping (`''`) which is error-prone
3. **Readability** - Code is much cleaner and easier to maintain
4. **Consistency** - Follows PostgreSQL best practices

### Basic Usage

```sql
-- ❌ BAD: Using single quotes with escaping (hard to read, no IDE highlighting)
EXECUTE format('SELECT * FROM %I WHERE name = ''%s''', v_table, v_name);

-- ✅ GOOD: Using dollar-quoting (readable, IDE highlights SQL syntax)
EXECUTE format($sql$SELECT * FROM %I WHERE name = '%s'$sql$, v_table, v_name);
```

### Nested Dollar-Quotes

When you need nested dollar-quotes, use labeled delimiters:

```sql
-- Using custom tags for nested quotes
DO $outer$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (SELECT content FROM pg_temp.pgmi_plan_view) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $outer$;

-- Complex nesting example
CREATE FUNCTION generate_migration() RETURNS TEXT AS $func$
DECLARE
    v_sql TEXT;
BEGIN
    v_sql := $migration$
        CREATE TABLE users (
            id SERIAL PRIMARY KEY,
            email TEXT NOT NULL,
            created_at TIMESTAMPTZ DEFAULT now()
        );
    $migration$;
    RETURN v_sql;
END;
$func$ LANGUAGE plpgsql;
```

### Apply Everywhere

Use dollar-quoting for:
- ✅ PL/pgSQL function bodies
- ✅ Multi-line string literals
- ✅ Dynamic SQL generation
- ✅ Test assertion messages
- ✅ DO blocks with EXECUTE

---

## Direct Execution Model

pgmi uses a **direct execution** model: your deploy.sql queries `pgmi_plan_view` and uses `EXECUTE` to run files. There is no intermediate plan table.

### Core Pattern

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

### Available Session Objects

| Object | Type | Purpose |
|--------|------|---------|
| `pgmi_plan_view` | VIEW | Files ordered by metadata for execution |
| `pgmi_source` | TABLE | All loaded files with path, content, metadata |
| `pgmi_test_source` | TABLE | Test files from `__test__/` directories |
| `pgmi_parameter` | TABLE | CLI parameters |
| `pgmi_declare_param()` | FUNCTION | Declare typed parameter with validation |
| `pgmi_get_param()` | FUNCTION | Get parameter value with default |
| `pgmi_test_plan()` | FUNCTION | Get test execution plan |
| `pgmi_test()` | MACRO | Preprocessor macro for running tests |

---

### Phased Deployment Example

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- Phase 1: Schemas
    RAISE NOTICE '=== Phase 1: Schemas ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './schemas/%' ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

    -- Phase 2: Migrations
    RAISE NOTICE '=== Phase 2: Migrations ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%' ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### Conditional Execution Example

```sql
DO $$
DECLARE
    v_file RECORD;
    v_env TEXT := pg_temp.pgmi_get_param('env', 'development');
BEGIN
    -- Always run migrations
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%' ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

    -- Only seed in development
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

---

### `pgmi_test()` Preprocessor Macro

**Purpose:** Execute unit tests with savepoint isolation

The `pgmi_test()` call is a **preprocessor macro** expanded by Go before SQL reaches PostgreSQL. Use `CALL` syntax:

```sql
-- Run all tests
CALL pgmi_test();

-- Run tests matching pattern (POSIX regex)
CALL pgmi_test('.*/integration/.*');
CALL pgmi_test('.*_critical\.sql$');
```

### `pgmi_test_plan(pattern)` Function

**Purpose:** Get test execution plan (for introspection)

Returns TABLE with columns: `ordinal`, `step_type`, `script_path`, `directory`, `depth`

```sql
-- See what tests would run
SELECT * FROM pg_temp.pgmi_test_plan();

-- Filter by pattern
SELECT * FROM pg_temp.pgmi_test_plan('.*/auth/.*');
```

---

### `pgmi_get_param(key TEXT, default_value TEXT DEFAULT NULL)`

**Purpose:** Access parameter with optional fallback

```sql
-- Get with default
v_env := pg_temp.pgmi_get_param('env', 'development');

-- Conditional logic
IF pg_temp.pgmi_get_param('enable_feature', 'false') = 'true' THEN
    -- Feature-specific code
END IF;

-- Environment-specific configuration
v_timeout := CASE pg_temp.pgmi_get_param('env', 'dev')
    WHEN 'production' THEN '60s'
    WHEN 'staging' THEN '30s'
    ELSE '10s'
END;
```

---

### `pgmi_declare_param(...)`

**Purpose:** Declare typed parameter with validation and defaults

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

---

## Testing Philosophy

### Pure PostgreSQL, Fail-Fast

**Core Principles:**
1. pgmi does NOT provide any assertion framework or testing DSL
2. Tests are pure SQL/PL/pgSQL using standard PostgreSQL error handling
3. Failed assertions use `RAISE EXCEPTION` - deployment stops immediately
4. No custom PASS/FAIL strings, no result tables, no test runners
5. Tests succeed silently (or with `RAISE NOTICE`), fail loudly with exceptions
6. Use `RAISE DEBUG` for diagnostic breadcrumbs (visible only with `pgmi --verbose`)

**Rationale:**
- Users already know PostgreSQL error handling (no learning curve)
- Deployment stops on first failure (fail-fast)
- Error messages appear naturally in output
- No framework to maintain, document, or explain
- Works seamlessly with PostgreSQL's transactional semantics

### Test Examples

Tests succeed silently, fail with exceptions:

#### ❌ BAD: Custom assertion framework
```sql
-- Don't do this - custom PASS/FAIL strings
SELECT CASE
    WHEN result = expected THEN 'PASS'
    ELSE 'FAIL'
END AS test_result;

-- Don't do this - result tables
INSERT INTO test_results (test_name, status) VALUES ('test_foo', 'PASS');
```

#### ✅ GOOD: PostgreSQL exceptions
```sql
-- Using CASE with error() function (if available)
SELECT CASE
    WHEN public.execute_migration_script(...) = -1 THEN true
    ELSE (SELECT error('Re-execution should return -1 (skipped)'))
END;

-- Direct assertion in PL/pgSQL
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM migration_script WHERE path = 'test.sql') THEN
        RAISE EXCEPTION 'TEST FAILED: Migration not tracked';
    END IF;
END $$;

-- Using assert (PostgreSQL 9.5+)
SELECT assert(
    COUNT(*) = 5,
    'Expected 5 users after migration'
) FROM users;

-- Complex validation
DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM api.http_route;
    IF v_count = 0 THEN
        RAISE EXCEPTION 'TEST FAILED: No routes registered';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM api.http_route WHERE route_name = 'hello_world') THEN
        RAISE EXCEPTION 'TEST FAILED: hello_world route not found';
    END IF;

    RAISE NOTICE 'All route tests passed (% routes)', v_count;
END $$;
```

### Inline Tests vs `__test__/` Tests

**Two testing patterns in pgmi:**

#### 1. Inline Tests (Pure Functions)
For functions with **no side effects** and **no data queries**:

```sql
-- utils/text_utils.sql
CREATE OR REPLACE FUNCTION utils.slugify(input_text TEXT)
RETURNS TEXT AS $$
BEGIN
    RETURN lower(regexp_replace(input_text, '[^a-zA-Z0-9]+', '-', 'g'));
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- Inline test (runs immediately, not transactional)
SELECT CASE
    WHEN utils.slugify('Hello World!') = 'hello-world-'
    THEN true
    ELSE (SELECT error('Slugify test failed'))
END;

-- More inline tests
DO $$
BEGIN
    IF utils.slugify('') != '' THEN
        RAISE EXCEPTION 'Empty string should return empty';
    END IF;

    IF utils.slugify('NoSpaces') != 'nospaces' THEN
        RAISE EXCEPTION 'No spaces should be preserved';
    END IF;

    RAISE NOTICE '✓ All slugify tests passed';
END $$;
```

**When to use inline tests:**
- ✅ Pure utility functions (IMMUTABLE, STABLE)
- ✅ Type conversion functions
- ✅ String manipulation, math, date helpers
- ✅ Functions with no external dependencies

**When NOT to use inline tests:**
- ❌ Functions that query tables
- ❌ Functions that modify data
- ❌ Functions with side effects
- ❌ Schema-dependent logic

#### 2. `__test__/` Tests (Transactional)
For tests that **manipulate schema** or **query data**:

```sql
-- __test__/test_migrations_tracking.sql
DO $$
DECLARE
    v_script_id UUID := 'test-uuid-1234';
    v_count INT;
BEGIN
    -- Insert test data
    INSERT INTO internal.deployment_script_execution_log (script_id, path, idempotent)
    VALUES (v_script_id, 'test.sql', false);

    -- Verify tracking
    SELECT COUNT(*) INTO v_count
    FROM internal.deployment_script_execution_log
    WHERE script_id = v_script_id;

    IF v_count != 1 THEN
        RAISE EXCEPTION 'TEST FAILED: Script not tracked (expected 1, got %)', v_count;
    END IF;

    -- Test idempotency check
    IF EXISTS (
        SELECT 1 FROM internal.deployment_script_execution_log
        WHERE script_id = v_script_id AND idempotent = true
    ) THEN
        RAISE EXCEPTION 'TEST FAILED: Idempotent flag should be false';
    END IF;

    RAISE NOTICE '✓ Migration tracking tests passed';
END $$;
```

**When to use `__test__/` tests:**
- ✅ Schema manipulation (CREATE TABLE, ALTER, etc.)
- ✅ Data insertion/modification for testing
- ✅ Integration tests (multiple components)
- ✅ Tests requiring rollback (no side effects)

**Isolation:**
- Tests in `__test__/` are automatically isolated by pgmi
- NOT executed during `pgmi deploy` (only via `pgmi test`)
- Run in transaction with automatic rollback
- Reason: Prevents accidental data corruption during deployment

---

## SQL vs PL/pgSQL Decision Guide

### When to Use SQL (Prefer)

**Use pure SQL when:**
- ✅ Single query achieves the goal
- ✅ Set-based operations (JOIN, GROUP BY, window functions)
- ✅ Data transformations
- ✅ Simple conditional logic (CASE, COALESCE)
- ✅ CTEs provide sufficient structure

**Benefits:**
- More concise
- Better query optimization by PostgreSQL
- Easier to read and maintain
- Less error-prone

**Example: Prefer SQL**
```sql
-- ✅ GOOD: Pure SQL
SELECT
    route_name,
    handler_function_name,
    CASE
        WHEN auto_log THEN 'logged'
        ELSE 'silent'
    END AS logging_mode
FROM api.http_route
WHERE method_regexp ~ '^GET$'
ORDER BY sequence_number;

-- ❌ AVOID: Unnecessary PL/pgSQL
DO $$
DECLARE
    v_route RECORD;
BEGIN
    FOR v_route IN SELECT * FROM api.http_route LOOP
        -- Procedural logic where SQL would suffice
    END LOOP;
END $$;
```

### When to Use PL/pgSQL (When Necessary)

**Use PL/pgSQL when:**
- ✅ Need variables and state
- ✅ Complex control flow (loops, conditionals with side effects)
- ✅ Exception handling required
- ✅ Dynamic SQL generation
- ✅ Multi-step transactions with logic between steps

**Example: PL/pgSQL Justified**
```sql
-- Complex orchestration with state and error handling
DO $$
DECLARE
    v_file RECORD;
    v_executed INT := 0;
    v_skipped INT := 0;
BEGIN
    FOR v_file IN (
        SELECT path, content, checksum, id, idempotent
        FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    ) LOOP
        BEGIN
            -- Check if already executed (if tracking is enabled)
            IF EXISTS (
                SELECT 1 FROM internal.deployment_script_execution_log
                WHERE script_id = v_file.id
                  AND (NOT v_file.idempotent OR checksum = v_file.checksum)
            ) THEN
                v_skipped := v_skipped + 1;
                CONTINUE;
            END IF;

            -- Execute file directly
            EXECUTE v_file.content;
            v_executed := v_executed + 1;

        EXCEPTION WHEN others THEN
            RAISE WARNING 'Failed to process %: %', v_file.path, SQLERRM;
            -- Continue or re-raise based on policy
        END;
    END LOOP;

    RAISE NOTICE 'Executed: %, Skipped: %', v_executed, v_skipped;
END $$;
```

---

## JSON Naming Conventions

**MANDATORY: All JSON/JSONB keys in pgmi use camelCase.**

JSON is a data interchange format with its own conventions. PostgreSQL identifiers use snake_case, but JSON content follows JSON/JavaScript conventions.

### The Rule

```sql
-- ❌ WRONG: snake_case in JSON keys (PostgreSQL habit)
jsonb_build_object(
    'http_method', '^GET$',
    'auto_log', false,
    'response_headers', '{}'::jsonb
)

-- ✅ CORRECT: camelCase in JSON keys (JSON convention)
jsonb_build_object(
    'httpMethod', '^GET$',
    'autoLog', false,
    'responseHeaders', '{}'::jsonb
)
```

### Why This Matters

1. **JSON is not PostgreSQL** - JSON originated from JavaScript, uses camelCase universally
2. **Ecosystem consistency** - GitHub API, Stripe, OpenAPI, MCP protocol all use camelCase
3. **Consumer ergonomics** - JavaScript/TypeScript consumers expect camelCase
4. **Internal consistency** - pgmi's MCP protocol already uses camelCase (`inputSchema`, `mimeType`, `isError`)

### Where This Applies

| Context | Convention | Example |
|---------|------------|---------|
| PostgreSQL identifiers | snake_case | `handler_function_name`, `api.rest_route` |
| PostgreSQL columns | snake_case | `created_at`, `object_id` |
| JSONB keys | **camelCase** | `httpMethod`, `autoLog`, `inputSchema` |
| JSON API responses | **camelCase** | `userId`, `createdAt`, `isActive` |

### Handler Registration Metadata Keys

Standard keys for handler registration (all camelCase):

```sql
-- REST handlers
jsonb_build_object(
    'id', '<uuid>',
    'uri', '^/path$',
    'httpMethod', '^GET$',
    'name', 'handler_name',
    'description', 'Handler description',
    'autoLog', true,
    'accepts', jsonb_build_array('application/json'),
    'produces', jsonb_build_array('application/json'),
    'responseHeaders', '{}'::jsonb
)

-- RPC handlers
jsonb_build_object(
    'id', '<uuid>',
    'methodName', 'namespace.method',
    'description', 'Method description',
    'autoLog', true
)

-- MCP handlers
jsonb_build_object(
    'id', '<uuid>',
    'type', 'tool',
    'name', 'tool_name',
    'description', 'Tool description',
    'inputSchema', jsonb_build_object(...),
    'uriTemplate', 'protocol:///{path}',
    'mimeType', 'application/json',
    'arguments', jsonb_build_array(...)
)
```

### No Exceptions

Do not mix conventions. Even if it "looks more PostgreSQL-like", use camelCase for all JSON keys:

```sql
-- ❌ NEVER mix conventions
jsonb_build_object(
    'method_name', 'foo',      -- snake_case ❌
    'inputSchema', '{}'        -- camelCase ✓ (inconsistent!)
)

-- ✅ ALWAYS consistent camelCase
jsonb_build_object(
    'methodName', 'foo',       -- camelCase ✓
    'inputSchema', '{}'        -- camelCase ✓
)
```

---

## Common SQL Patterns

### Idempotent Functions

Functions that can be safely re-executed:

```sql
CREATE OR REPLACE FUNCTION api.handle_hello_world(p_request jsonb)
RETURNS jsonb AS $$
BEGIN
    RETURN jsonb_build_object(
        'status', 200,
        'body', jsonb_build_object('message', 'Hello, World!')
    );
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- Idempotent: CREATE OR REPLACE ensures safe re-execution
-- Safe to mark idempotent="true" in metadata
```

### Conditional DDL

Schema changes that check before executing:

```sql
-- Create schema if not exists
CREATE SCHEMA IF NOT EXISTS api;

-- Add column only if missing
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'users'
          AND column_name = 'deleted_at'
    ) THEN
        ALTER TABLE public.users ADD COLUMN deleted_at TIMESTAMPTZ;
    END IF;
END $$;

-- Create index idempotently
CREATE INDEX IF NOT EXISTS idx_users_email ON public.users(email);

-- Add constraint if missing
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'users_email_unique'
    ) THEN
        ALTER TABLE public.users ADD CONSTRAINT users_email_unique UNIQUE (email);
    END IF;
END $$;
```

### Transaction Control Patterns

**Pattern 1: Single Transaction (All-or-Nothing)**
```sql
-- deploy.sql: Single transaction wraps all execution
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

**Pattern 2: Phased Commits**
```sql
-- deploy.sql: Separate transactions per phase

-- Phase 1: Schema setup
BEGIN;
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_plan_view WHERE path = './init.sql'
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;

-- Phase 2: Migrations
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

-- Phase 3: Tests (with automatic rollback via macro)
BEGIN;
pgmi_test();
COMMIT;
```

**Pattern 3: Per-File Error Handling**
```sql
-- Error handling during execution
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
        BEGIN
            EXECUTE v_file.content;
        EXCEPTION WHEN others THEN
            RAISE WARNING 'Failed to execute %: %', v_file.path, SQLERRM;
            -- Continue or re-raise based on policy
        END;
    END LOOP;
END $$;

COMMIT;
```

### Error Handling Patterns

**Pattern 1: Graceful Degradation**
```sql
DO $$
BEGIN
    -- Try optional feature
    PERFORM install_optional_extension();
EXCEPTION
    WHEN others THEN
        RAISE NOTICE 'Optional feature unavailable: %', SQLERRM;
        -- Continue deployment
END $$;
```

**Pattern 2: Detailed Error Context**
```sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (SELECT path, content FROM pg_temp.pgmi_source WHERE is_sql_file ORDER BY path) LOOP
        BEGIN
            EXECUTE v_file.content;
        EXCEPTION WHEN others THEN
            RAISE EXCEPTION 'Failed to execute %: % (SQLSTATE: %)',
                v_file.path, SQLERRM, SQLSTATE;
        END;
    END LOOP;
END $$;
```

**Pattern 3: Retry Logic**
```sql
DO $$
DECLARE
    v_attempts INT := 0;
    v_max_attempts INT := 3;
    v_success BOOLEAN := false;
BEGIN
    WHILE v_attempts < v_max_attempts AND NOT v_success LOOP
        BEGIN
            v_attempts := v_attempts + 1;

            -- Risky operation
            PERFORM acquire_lock_and_process();
            v_success := true;

        EXCEPTION WHEN lock_not_available THEN
            IF v_attempts >= v_max_attempts THEN
                RAISE EXCEPTION 'Failed after % attempts', v_max_attempts;
            END IF;

            RAISE NOTICE 'Attempt % failed, retrying...', v_attempts;
            PERFORM pg_sleep(1); -- Backoff
        END;
    END LOOP;

    RAISE NOTICE 'Success after % attempts', v_attempts;
END $$;
```

---

## Error Context Capture with GET STACKED DIAGNOSTICS

PostgreSQL's `GET STACKED DIAGNOSTICS` statement captures comprehensive error context in `EXCEPTION` blocks, essential for debugging and structured error tracking.

### Available Diagnostic Variables

| Variable | Description | Example Value |
|----------|-------------|---------------|
| `RETURNED_SQLSTATE` | PostgreSQL error code | `'23505'` (unique_violation) |
| `MESSAGE_TEXT` | Primary error message | `'duplicate key value violates unique constraint "users_email_key"'` |
| `PG_EXCEPTION_DETAIL` | Additional details | `'Key (email)=(user@example.com) already exists.'` |
| `PG_EXCEPTION_HINT` | Suggested resolution | `'Use ON CONFLICT clause to handle duplicates.'` |
| `PG_EXCEPTION_CONTEXT` | Stack trace | `'PL/pgSQL function api.create_user(jsonb) line 15 at SQL statement'` |

**All variables return `text` and are only available within `EXCEPTION` handlers.**

### Pattern: Structured Error Capture

Create a reusable function to capture full error context:

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
        'message', v_message,
        'detail', v_detail,
        'hint', v_hint,
        'context', v_context,
        'timestamp', now()
    );
END;
$$ LANGUAGE plpgsql;
```

**Usage in exception handler:**

```sql
DO $$
BEGIN
    -- Risky operation
    INSERT INTO users (email) VALUES ('duplicate@example.com');

EXCEPTION
    WHEN OTHERS THEN
        DECLARE
            v_error_context jsonb;
        BEGIN
            v_error_context := api.build_error_context();

            -- Log structured error
            INSERT INTO error_log (error_data, occurred_at)
            VALUES (v_error_context, now());

            -- Or raise warning with full context
            RAISE WARNING 'Operation failed: %', v_error_context;

            -- Or examine specific fields
            RAISE NOTICE 'Error: % (SQLSTATE: %)',
                v_error_context->>'message',
                v_error_context->>'sqlstate';
        END;
END $$;
```

**Example Output:**
```json
{
  "sqlstate": "23505",
  "message": "duplicate key value violates unique constraint \"users_email_key\"",
  "detail": "Key (email)=(duplicate@example.com) already exists.",
  "hint": null,
  "context": "SQL statement \"INSERT INTO users (email) VALUES ('duplicate@example.com')\"\nPL/pgSQL function inline_code_block line 3 at SQL statement",
  "timestamp": "2025-01-15T10:30:45.123Z"
}
```

### Pattern: SQLSTATE-Based Error Classification

Different SQLSTATE codes require different handling strategies. Classify errors to enable appropriate responses:

```sql
-- Classify errors into actionable categories
CREATE OR REPLACE FUNCTION classify_error(p_sqlstate text)
RETURNS text AS $$
    SELECT CASE
        -- Transient errors (safe to retry)
        WHEN $1 LIKE '08%' THEN 'connection_failure'       -- Connection exception
        WHEN $1 IN ('40001', '40P01') THEN 'serialization_conflict'  -- Deadlock, serialization
        WHEN $1 = '55P03' THEN 'lock_timeout'              -- Lock timeout
        WHEN $1 IN ('57014', '57P01') THEN 'query_timeout' -- Query canceled, terminating

        -- Client errors (fix request and retry)
        WHEN $1 = '23505' THEN 'unique_violation'          -- Duplicate key
        WHEN $1 = '23503' THEN 'foreign_key_violation'     -- FK constraint
        WHEN $1 = '23514' THEN 'check_violation'           -- CHECK constraint
        WHEN $1 = '23502' THEN 'not_null_violation'        -- NOT NULL violation
        WHEN $1 LIKE '22%' THEN 'data_exception'           -- Invalid data format

        -- Server errors (investigate)
        ELSE 'internal_error'
    END;
$$ LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE;
```

**Usage with classification:**

```sql
DO $$
DECLARE
    v_retry_count INT := 0;
    v_max_retries INT := 3;
    v_success BOOLEAN := false;
BEGIN
    WHILE v_retry_count < v_max_retries AND NOT v_success LOOP
        BEGIN
            v_retry_count := v_retry_count + 1;

            -- Risky operation prone to deadlocks
            UPDATE accounts SET balance = balance - 100 WHERE account_id = 'A';
            UPDATE accounts SET balance = balance + 100 WHERE account_id = 'B';

            v_success := true;

        EXCEPTION
            WHEN OTHERS THEN
                DECLARE
                    v_sqlstate text;
                    v_error_class text;
                    v_error_context jsonb;
                BEGIN
                    GET STACKED DIAGNOSTICS v_sqlstate = RETURNED_SQLSTATE;
                    v_error_class := classify_error(v_sqlstate);
                    v_error_context := api.build_error_context();

                    -- Handle based on classification
                    CASE v_error_class
                        WHEN 'serialization_conflict' THEN
                            IF v_retry_count >= v_max_retries THEN
                                RAISE EXCEPTION 'Transient error after % attempts: %',
                                    v_max_retries, v_error_context;
                            END IF;
                            RAISE NOTICE 'Attempt % failed (transient), retrying...', v_retry_count;
                            PERFORM pg_sleep(0.1 * v_retry_count); -- Exponential backoff

                        WHEN 'unique_violation' THEN
                            RAISE EXCEPTION 'Client error (non-retryable): %', v_error_context;

                        WHEN 'connection_failure' THEN
                            IF v_retry_count >= v_max_retries THEN
                                RAISE EXCEPTION 'Connection failed after % attempts: %',
                                    v_max_retries, v_error_context;
                            END IF;
                            RAISE NOTICE 'Connection lost, retrying...';
                            PERFORM pg_sleep(1); -- Longer backoff for connection issues

                        ELSE
                            RAISE EXCEPTION 'Server error (investigate): %', v_error_context;
                    END CASE;
                END;
        END;
    END LOOP;

    IF v_success THEN
        RAISE NOTICE 'Operation succeeded after % attempts', v_retry_count;
    END IF;
END $$;
```

**Classification Benefits:**
- **Transient errors** (connection_failure, serialization_conflict, lock_timeout, query_timeout) → Retry with backoff
- **Client errors** (unique_violation, foreign_key_violation, check_violation, not_null_violation, data_exception) → Fail fast, user must fix request
- **Server errors** (internal_error) → Fail fast, investigate and fix server-side issue

### Pattern: Structured Error History (jsonb Array)

Track errors across retry attempts using jsonb arrays. Essential for debugging intermittent issues:

```sql
-- Create error tracking table
CREATE TABLE IF NOT EXISTS task_queue (
    task_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_data jsonb NOT NULL,
    processing_attempts INT DEFAULT 0,
    error_history jsonb,  -- Array of error objects
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now()
);
```

**Accumulate errors on each retry:**

```sql
DO $$
DECLARE
    v_task RECORD;
    v_error_context jsonb;
BEGIN
    -- Lock next task for processing
    SELECT * INTO v_task
    FROM task_queue
    WHERE completed_at IS NULL
      AND processing_attempts < 5
    ORDER BY created_at
    LIMIT 1
    FOR UPDATE SKIP LOCKED;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    BEGIN
        -- Update attempt counter
        UPDATE task_queue
        SET processing_attempts = processing_attempts + 1
        WHERE task_id = v_task.task_id;

        -- Process task
        PERFORM process_task(v_task.task_data);

        -- Mark complete on success
        UPDATE task_queue
        SET completed_at = now()
        WHERE task_id = v_task.task_id;

    EXCEPTION
        WHEN OTHERS THEN
            -- Capture error context
            v_error_context := api.build_error_context();

            -- Accumulate error history (don't overwrite)
            UPDATE task_queue
            SET error_history = COALESCE(error_history, '[]'::jsonb)
                             || jsonb_build_array(
                                 v_error_context || jsonb_build_object(
                                     'attempt', processing_attempts + 1,
                                     'timestamp', now()
                                 )
                             )
            WHERE task_id = v_task.task_id;

            RAISE WARNING 'Task % failed (attempt %): [%] %',
                v_task.task_id,
                v_task.processing_attempts + 1,
                v_error_context->>'sqlstate',
                v_error_context->>'message';
    END;
END $$;
```

**Query error history:**

```sql
-- Find tasks with repeated errors
SELECT
    task_id,
    processing_attempts,
    jsonb_array_length(error_history) as error_count,
    error_history->-1->>'message' as last_error_message,
    error_history->-1->>'sqlstate' as last_sqlstate,
    error_history->-1->>'timestamp' as last_error_time
FROM task_queue
WHERE error_history IS NOT NULL
ORDER BY processing_attempts DESC;

-- Analyze error progression for specific task
SELECT
    task_id,
    jsonb_array_elements(error_history) as error_attempt
FROM task_queue
WHERE task_id = '<task-uuid>';

-- Find tasks failing with specific error class
SELECT
    task_id,
    processing_attempts,
    error_history->-1->>'sqlstate' as error_code,
    error_history->-1->>'message' as error_message
FROM task_queue
WHERE error_history IS NOT NULL
  AND error_history->-1->>'sqlstate' LIKE '40%'  -- Serialization conflicts
ORDER BY created_at DESC;

-- Compute error statistics
SELECT
    error_history->-1->>'sqlstate' as sqlstate,
    COUNT(*) as occurrences,
    AVG(processing_attempts) as avg_attempts,
    MAX(processing_attempts) as max_attempts
FROM task_queue
WHERE error_history IS NOT NULL
GROUP BY error_history->-1->>'sqlstate'
ORDER BY occurrences DESC;
```

### Pattern: HTTP Status Code Mapping

When building HTTP APIs backed by PostgreSQL, map SQLSTATE to appropriate HTTP status codes:

```sql
CREATE OR REPLACE FUNCTION sqlstate_to_http_status(p_sqlstate text)
RETURNS integer AS $$
    SELECT CASE classify_error($1)
        -- Transient errors → 5xx (server should retry)
        WHEN 'connection_failure' THEN 503        -- Service Unavailable
        WHEN 'serialization_conflict' THEN 503    -- Service Unavailable (retry)
        WHEN 'lock_timeout' THEN 503              -- Service Unavailable (retry)
        WHEN 'query_timeout' THEN 504             -- Gateway Timeout

        -- Client errors → 4xx (client must fix)
        WHEN 'unique_violation' THEN 409          -- Conflict
        WHEN 'foreign_key_violation' THEN 400     -- Bad Request
        WHEN 'check_violation' THEN 400           -- Bad Request
        WHEN 'not_null_violation' THEN 400        -- Bad Request
        WHEN 'data_exception' THEN 400            -- Bad Request

        -- Server errors → 500
        ELSE 500                                  -- Internal Server Error
    END;
$$ LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE;
```

**Usage in HTTP handler:**

```sql
CREATE OR REPLACE FUNCTION api.create_user(p_request jsonb)
RETURNS jsonb AS $$
DECLARE
    v_email text;
    v_user_id uuid;
BEGIN
    v_email := p_request->>'email';

    -- Insert user
    INSERT INTO users (email) VALUES (v_email)
    RETURNING id INTO v_user_id;

    -- Success response
    RETURN jsonb_build_object(
        'status', 200,
        'body', jsonb_build_object('user_id', v_user_id)
    );

EXCEPTION
    WHEN OTHERS THEN
        DECLARE
            v_error_context jsonb;
            v_http_status integer;
        BEGIN
            v_error_context := api.build_error_context();
            v_http_status := sqlstate_to_http_status(v_error_context->>'sqlstate');

            RETURN jsonb_build_object(
                'status', v_http_status,
                'headers', jsonb_build_object(
                    'X-Error-Class', classify_error(v_error_context->>'sqlstate'),
                    'X-SQLSTATE', v_error_context->>'sqlstate'
                ),
                'body', jsonb_build_object(
                    'error', v_error_context->>'message',
                    'detail', v_error_context->>'detail'
                )
            );
        END;
END;
$$ LANGUAGE plpgsql;
```

**Example Responses:**

```json
// Unique violation (409 Conflict)
{
  "status": 409,
  "headers": {"X-Error-Class": "unique_violation", "X-SQLSTATE": "23505"},
  "body": {
    "error": "duplicate key value violates unique constraint \"users_email_key\"",
    "detail": "Key (email)=(user@example.com) already exists."
  }
}

// Serialization conflict (503 Service Unavailable)
{
  "status": 503,
  "headers": {"X-Error-Class": "serialization_conflict", "X-SQLSTATE": "40P01"},
  "body": {
    "error": "deadlock detected",
    "detail": "Process 12345 waits for ShareLock on transaction 67890; blocked by process 23456."
  }
}
```

### Best Practices

**1. Always capture full context**
```sql
-- ❌ BAD: Using only SQLERRM (incomplete)
EXCEPTION
    WHEN OTHERS THEN
        RAISE WARNING 'Error: %', SQLERRM;

-- ✅ GOOD: Capture all diagnostics
EXCEPTION
    WHEN OTHERS THEN
        DECLARE
            v_error_context jsonb;
        BEGIN
            v_error_context := api.build_error_context();
            RAISE WARNING 'Error: %', v_error_context;
        END;
```

**2. Classify by SQLSTATE, not message**
```sql
-- ❌ BAD: String matching on error message (fragile)
EXCEPTION
    WHEN OTHERS THEN
        IF SQLERRM LIKE '%duplicate key%' THEN
            -- Handle uniqueness violation
        END IF;

-- ✅ GOOD: Use SQLSTATE (stable across PostgreSQL versions)
EXCEPTION
    WHEN OTHERS THEN
        DECLARE
            v_sqlstate text;
        BEGIN
            GET STACKED DIAGNOSTICS v_sqlstate = RETURNED_SQLSTATE;
            IF v_sqlstate = '23505' THEN  -- unique_violation
                -- Handle uniqueness violation
            END IF;
        END;
```

**3. Structure your errors (use jsonb)**
```sql
-- ❌ BAD: Plain text error storage (hard to query)
UPDATE task_queue
SET last_error = SQLERRM
WHERE task_id = v_task_id;

-- ✅ GOOD: Structured jsonb (queryable, extensible)
UPDATE task_queue
SET error_history = COALESCE(error_history, '[]'::jsonb)
                 || jsonb_build_array(api.build_error_context())
WHERE task_id = v_task_id;
```

**4. Include timestamps**
```sql
-- ✅ GOOD: Always timestamp errors
RETURN jsonb_build_object(
    'sqlstate', v_sqlstate,
    'message', v_message,
    'detail', v_detail,
    'hint', v_hint,
    'context', v_context,
    'timestamp', now(),  -- Essential for debugging race conditions
    'attempt', v_attempt_number
);
```

**5. Preserve error history (don't overwrite)**
```sql
-- ❌ BAD: Overwrites previous errors
UPDATE task_queue
SET error_data = v_new_error
WHERE task_id = v_task_id;

-- ✅ GOOD: Accumulates error history
UPDATE task_queue
SET error_history = COALESCE(error_history, '[]'::jsonb)
                 || jsonb_build_array(v_new_error)
WHERE task_id = v_task_id;
```

**6. Use indexes for error queries**
```sql
-- Enable efficient JSON queries
CREATE INDEX idx_task_queue_errors
    ON task_queue USING gin(error_history)
    WHERE error_history IS NOT NULL;

-- Fast queries on error history
SELECT * FROM task_queue
WHERE error_history @> '[{"sqlstate": "23505"}]';
```

### Common SQLSTATE Codes Reference

| Code | Class | Description | Handling |
|------|-------|-------------|----------|
| `08xxx` | Connection Exception | Connection failure, connection does not exist | Retry with backoff |
| `23502` | Integrity Constraint Violation | NOT NULL violation | Client fix (400) |
| `23503` | Integrity Constraint Violation | Foreign key violation | Client fix (400) |
| `23505` | Integrity Constraint Violation | Unique violation | Client fix (409) |
| `23514` | Integrity Constraint Violation | CHECK violation | Client fix (400) |
| `22xxx` | Data Exception | Invalid data format (date, number, etc.) | Client fix (400) |
| `40001` | Transaction Rollback | Serialization failure | Retry (503) |
| `40P01` | Transaction Rollback | Deadlock detected | Retry (503) |
| `55P03` | Object Not In Prerequisite State | Lock not available (timeout) | Retry with backoff (503) |
| `57014` | Query Canceled | Statement timeout, cancel request | Retry or optimize (504) |
| `57P01` | Admin Shutdown | Server shutting down | Retry (503) |

**Full reference:** https://www.postgresql.org/docs/current/errcodes-appendix.html

---

## Best Practices

### 1. Use Explicit Types
```sql
-- ❌ AVOID: Implicit casting
CREATE FUNCTION foo(p_id TEXT) ...

-- ✅ GOOD: Explicit types matching usage
CREATE FUNCTION foo(p_id UUID) ...
CREATE FUNCTION bar(p_count INT) ...
CREATE FUNCTION baz(p_data jsonb) ...
```

### 2. Null Handling
```sql
-- ❌ AVOID: Forgetting NULL cases
SELECT user_name FROM users; -- What if NULL?

-- ✅ GOOD: Explicit NULL handling
SELECT COALESCE(user_name, 'Anonymous') AS user_name FROM users;

-- ✅ GOOD: NULL checks in functions
CREATE FUNCTION process_name(p_name TEXT) RETURNS TEXT AS $$
BEGIN
    IF p_name IS NULL THEN
        RAISE EXCEPTION 'Name cannot be NULL';
    END IF;
    RETURN upper(p_name);
END;
$$ LANGUAGE plpgsql;
```

### 3. Use STRICT/RETURNS NULL ON NULL INPUT
```sql
-- Function returns NULL if any argument is NULL (no execution)
CREATE FUNCTION add_numbers(a INT, b INT)
RETURNS INT AS $$
BEGIN
    RETURN a + b;
END;
$$ LANGUAGE plpgsql STRICT;

-- Equivalent to:
CREATE FUNCTION add_numbers(a INT, b INT)
RETURNS INT AS $$
BEGIN
    RETURN a + b;
END;
$$ LANGUAGE plpgsql RETURNS NULL ON NULL INPUT;
```

### 4. Security: SECURITY DEFINER vs INVOKER
```sql
-- ❌ RISK: SECURITY DEFINER without validation
CREATE FUNCTION delete_user(p_user_id UUID)
RETURNS VOID AS $$
BEGIN
    DELETE FROM users WHERE id = p_user_id;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER; -- Runs as owner!

-- ✅ SAFE: SECURITY DEFINER with validation
CREATE FUNCTION delete_user(p_user_id UUID)
RETURNS VOID AS $$
BEGIN
    -- Verify caller has permission
    IF NOT is_admin(current_user) THEN
        RAISE EXCEPTION 'Permission denied';
    END IF;

    DELETE FROM users WHERE id = p_user_id;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- ✅ SAFE: SECURITY INVOKER (default, runs as caller)
CREATE FUNCTION get_user_info(p_user_id UUID)
RETURNS TABLE(name TEXT, email TEXT) AS $$
BEGIN
    RETURN QUERY SELECT name, email FROM users WHERE id = p_user_id;
END;
$$ LANGUAGE plpgsql SECURITY INVOKER;
```

### 5. Immutability Annotations
```sql
-- IMMUTABLE: Same input always returns same output, no DB access
CREATE FUNCTION slugify(input_text TEXT)
RETURNS TEXT AS $$
BEGIN
    RETURN lower(regexp_replace(input_text, '[^a-zA-Z0-9]+', '-', 'g'));
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- STABLE: Same input returns same output within transaction, can read DB
CREATE FUNCTION get_current_user_email()
RETURNS TEXT AS $$
BEGIN
    RETURN (SELECT email FROM users WHERE id = current_setting('app.user_id')::UUID);
END;
$$ LANGUAGE plpgsql STABLE;

-- VOLATILE: May return different results, may modify DB (default)
CREATE FUNCTION log_access(p_resource TEXT)
RETURNS VOID AS $$
BEGIN
    INSERT INTO access_log (resource, accessed_at) VALUES (p_resource, now());
END;
$$ LANGUAGE plpgsql VOLATILE;
```

### 6. Performance: Use Set-Returning Functions Wisely
```sql
-- ❌ SLOW: Row-by-row processing
CREATE FUNCTION get_active_users()
RETURNS TABLE(id UUID, name TEXT) AS $$
DECLARE
    v_user RECORD;
BEGIN
    FOR v_user IN SELECT * FROM users LOOP
        IF v_user.deleted_at IS NULL THEN
            id := v_user.id;
            name := v_user.name;
            RETURN NEXT;
        END IF;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- ✅ FAST: Set-based approach
CREATE FUNCTION get_active_users()
RETURNS TABLE(id UUID, name TEXT) AS $$
BEGIN
    RETURN QUERY
    SELECT u.id, u.name
    FROM users u
    WHERE u.deleted_at IS NULL;
END;
$$ LANGUAGE plpgsql STABLE;

-- ✅ BETTER: Pure SQL (no PL/pgSQL overhead)
CREATE VIEW active_users AS
SELECT id, name FROM users WHERE deleted_at IS NULL;
```

---

## Troubleshooting

### Common SQL Errors

**1. Syntax Error: Unexpected Token**
```
ERROR:  syntax error at or near "PERFORM"
```
**Cause:** Using PL/pgSQL keywords in SQL context
**Fix:** Wrap in DO block or function

**2. Dollar-Quote Mismatch**
```
ERROR:  unterminated dollar-quoted string
```
**Cause:** Mismatched dollar-quote tags
**Fix:** Ensure opening and closing tags match exactly

```sql
-- ❌ BAD
DO $outer$
BEGIN
    ...
END $inner$; -- Mismatch!

-- ✅ GOOD
DO $outer$
BEGIN
    ...
END $outer$;
```

**3. Format Placeholder Error**
```
ERROR:  too few arguments for format()
```
**Cause:** Incorrect format() usage with placeholders
**Fix:** Use %s, %I, %L placeholders correctly

```sql
-- ❌ BAD: Wrong placeholder
EXECUTE format('SELECT * FROM %', v_table);

-- ✅ GOOD: Correct placeholder
EXECUTE format('SELECT * FROM %I', v_table);

-- ❌ BAD: Missing placeholder
RAISE NOTICE 'Value: %';

-- ✅ GOOD: Correct RAISE NOTICE usage
RAISE NOTICE 'Value: %', v_value;
```

**4. Variable Scope Issues**
```
ERROR:  column "v_count" does not exist
```
**Cause:** Variable used outside DECLARE block
**Fix:** Ensure variables declared in proper scope

```sql
-- ❌ BAD
DO $$
BEGIN
    SELECT COUNT(*) INTO v_count FROM users; -- v_count not declared!
END $$;

-- ✅ GOOD
DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM users;
    RAISE NOTICE 'Count: %', v_count;
END $$;
```

---

## Quick Reference

### Session API Summary

| Object | Type | Purpose |
|--------|------|---------|
| `pgmi_plan_view` | VIEW | Files ordered by metadata for direct EXECUTE |
| `pgmi_source` | TABLE | All loaded files with path, content, metadata |
| `pgmi_test_source` | TABLE | Test files from `__test__/` directories |
| `pgmi_parameter` | TABLE | CLI parameters |
| `pgmi_declare_param()` | FUNCTION | Declare typed parameter with validation |
| `pgmi_get_param(key, default)` | FUNCTION | Get parameter value with fallback |
| `pgmi_test_plan(pattern)` | FUNCTION | Get test execution plan |
| `pgmi_test()` | MACRO | Preprocessor macro for running tests |

### Format Placeholders

| Placeholder | Meaning | Example |
|-------------|---------|---------|
| `%s` | String value | `'hello'` → `hello` |
| `%I` | Identifier (quoted) | `my-table` → `"my-table"` |
| `%L` | Literal (escaped) | `O'Brien` → `'O''Brien'` |

### When to Use What

| Scenario | Tool |
|----------|------|
| Pure utility function | SQL, mark IMMUTABLE |
| Database query in function | SQL, mark STABLE |
| Function with side effects | PL/pgSQL, mark VOLATILE |
| Simple conditional | CASE expression in SQL |
| Complex logic with state | PL/pgSQL DO block |
| Tests with no side effects | Inline tests (after function) |
| Tests with side effects | `__test__/` directory |

---

## See Also

- **pgmi-templates skill:** Metadata format, advanced template architecture
- **CLAUDE.md:** Core philosophy, parameter system, development guidelines
- **Template READMEs:** User-facing deployment guides
- **PostgreSQL Documentation:** https://www.postgresql.org/docs/current/

