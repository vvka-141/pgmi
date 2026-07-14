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
- **Think in sets, not rows** — one `UPDATE` / `INSERT … SELECT` / `MERGE` over a set beats a `FOR … LOOP` (or cursor) that touches rows one at a time. A row-by-row loop for data work is an anti-pattern; loops are for orchestrating dynamic `EXECUTE` (e.g. deploy.sql iterating `pgmi_plan_view`), not for manipulating data.
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
CREATE TABLE "user" (...);
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
        CREATE TABLE "user" (
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

pgmi uses a two-tier API: internal tables (`_pgmi_*` prefix) and public views (`*_view` suffix).

| Object | Type | Purpose |
|--------|------|---------|
| `pgmi_plan_view` | VIEW | Files ordered by metadata for execution |
| `pgmi_source_view` | VIEW | All loaded files with path, content, metadata |
| `pgmi_test_source_view` | VIEW | Test files from `__test__/` directories |
| `pgmi_parameter_view` | VIEW | CLI parameters |
| `pgmi_test_plan()` | FUNCTION | Get test execution plan |
| `pgmi_test_generate()` | FUNCTION | Generate test SQL with savepoints |
| `CALL pgmi_test()` | MACRO | Preprocessor macro for running tests |
| `current_setting('pgmi.key', true)` | BUILT-IN | Access parameter (with COALESCE for default) |

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
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
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

### Parameter Access

**Purpose:** Access CLI parameters with optional fallback using PostgreSQL's native `current_setting()`

Parameters passed via `--param key=value` are automatically available as session configuration variables with the `pgmi.` prefix.

```sql
-- Get with default (second parameter `true` returns NULL if not set)
v_env := COALESCE(current_setting('pgmi.env', true), 'development');

-- Conditional logic
IF COALESCE(current_setting('pgmi.enable_feature', true), 'false') = 'true' THEN
    -- Feature-specific code
END IF;

-- Environment-specific configuration
v_timeout := CASE COALESCE(current_setting('pgmi.env', true), 'dev')
    WHEN 'production' THEN '60s'
    WHEN 'staging' THEN '30s'
    ELSE '10s'
END;

-- Required parameter (fails fast if not provided)
v_password := current_setting('pgmi.admin_password');
```

**Template-Specific Helpers:**
Templates can define their own helper functions for parameter access with validation and type coercion. For example, the advanced template provides a `deployment_setting()` helper.

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
- Uses PostgreSQL's transactional semantics directly

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
-- Inline assertion with DO block
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM migration_script WHERE path = 'test.sql') THEN
        RAISE EXCEPTION 'TEST FAILED: Migration not tracked';
    END IF;
END $$;

-- Using the ASSERT statement (PostgreSQL 9.5+)
DO $$
BEGIN
    ASSERT (SELECT COUNT(*) FROM "user") = 5, 'Expected 5 users after migration';
END $$;

-- Complex validation
DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM api.handler;
    IF v_count = 0 THEN
        RAISE EXCEPTION 'TEST FAILED: No routes registered';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM api.handler WHERE handler_function_name = 'hello_world') THEN
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
-- common/text.sql
CREATE OR REPLACE FUNCTION common.slugify(input_text TEXT)
RETURNS TEXT AS $$
BEGIN
    RETURN lower(regexp_replace(input_text, '[^a-zA-Z0-9]+', '-', 'g'));
END;
$$ LANGUAGE plpgsql IMMUTABLE;

-- Inline test (runs immediately, not transactional)
DO $$
BEGIN
    IF common.slugify('Hello World!') != 'hello-world-' THEN
        RAISE EXCEPTION 'Slugify test failed';
    END IF;
END $$;

-- More inline tests
DO $$
BEGIN
    IF common.slugify('') != '' THEN
        RAISE EXCEPTION 'Empty string should return empty';
    END IF;

    IF common.slugify('NoSpaces') != 'nospaces' THEN
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
    INSERT INTO example_script_log (script_id, path, idempotent)
    VALUES (v_script_id, 'test.sql', false);

    -- Verify tracking
    SELECT COUNT(*) INTO v_count
    FROM example_script_log
    WHERE script_id = v_script_id;

    IF v_count != 1 THEN
        RAISE EXCEPTION 'TEST FAILED: Script not tracked (expected 1, got %)', v_count;
    END IF;

    -- Test idempotency check
    IF EXISTS (
        SELECT 1 FROM example_script_log
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
- Executed via `pgmi_test()` macro in deploy.sql
- Run within savepoints with automatic rollback
- Reason: Test data never persists, migrations commit normally

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
    handler_function_name,
    handler_type,
    CASE WHEN requires_auth THEN 'protected' ELSE 'public' END AS auth_mode
FROM api.handler
WHERE handler_type = 'rest'
ORDER BY handler_function_name;

-- ❌ AVOID: Unnecessary PL/pgSQL
DO $$
DECLARE
    v_route RECORD;
BEGIN
    FOR v_route IN SELECT * FROM api.handler LOOP
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
                SELECT 1 FROM example_script_log
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
CALL pgmi_test();
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

> **⚠ SCOPE WARNING — not for API handler bodies.** The `EXCEPTION` / error-handling patterns in this section apply to **deploy-time orchestration** (`deploy.sql`, migrations, background/queue processing) and **gateway-level catch blocks** only. They do **not** belong inside REST/RPC/MCP handler bodies. A handler must signal every outcome by *returning* `api.problem_response(...)` via the four-phase pattern (materialize → validate → probe → execute) — never by throwing and catching. Catching an exception in a handler opens a per-request savepoint, produces the wrong HTTP status, and leaks `SQLERRM` internals to the client. See `pgmi-handler-patterns`.

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
    FOR v_file IN (SELECT path, content FROM pg_temp.pgmi_source_view WHERE is_sql_file ORDER BY path) LOOP
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

## Errors in deploy.sql

Generic PostgreSQL error handling (`GET STACKED DIAGNOSTICS`, SQLSTATE classes,
`EXCEPTION` blocks) is assumed knowledge — consult
https://www.postgresql.org/docs/current/errcodes-appendix.html when you need a code.

What is pgmi-specific is **attribution**: pgmi executes your files, so a bare
error tells the operator nothing about *which* file failed. Wrap execution and
name the file:

```sql
FOR v_file IN SELECT path, content FROM pg_temp.pgmi_plan_view ORDER BY execution_order
LOOP
    BEGIN
        EXECUTE v_file.content;
    EXCEPTION WHEN OTHERS THEN
        RAISE EXCEPTION 'Failed in %: %', v_file.path, SQLERRM
            USING ERRCODE = SQLSTATE,          -- keep the class; do not flatten it
                  DETAIL   = COALESCE(PG_EXCEPTION_DETAIL, '');
    END;
END LOOP;
```

Preserve `ERRCODE`: pgmi maps SQLSTATE to an exit code, and a caller (or a retry
loop) cannot classify a failure you have rewritten into a generic error.

For diagnosing a failed deploy from its exit code, load the `pgmi-debug-deploy`
skill.

---

---

## Best Practices


### 1. Use STRICT/RETURNS NULL ON NULL INPUT
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

### 2. Security: SECURITY DEFINER vs INVOKER
```sql
-- ❌ RISK: SECURITY DEFINER without validation
CREATE FUNCTION delete_user(p_user_id UUID)
RETURNS VOID AS $$
BEGIN
    DELETE FROM "user" WHERE id = p_user_id;
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

    DELETE FROM "user" WHERE id = p_user_id;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- ✅ SAFE: SECURITY INVOKER (default, runs as caller)
CREATE FUNCTION get_user_info(p_user_id UUID)
RETURNS TABLE(name TEXT, email TEXT) AS $$
BEGIN
    RETURN QUERY SELECT name, email FROM "user" WHERE id = p_user_id;
END;
$$ LANGUAGE plpgsql SECURITY INVOKER;
```

### 3. Immutability Annotations
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
    RETURN (SELECT email FROM "user" WHERE id = current_setting('app.user_id')::UUID);
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

### 4. Performance: Use Set-Returning Functions Wisely
```sql
-- ❌ SLOW: Row-by-row processing
CREATE FUNCTION get_active_users()
RETURNS TABLE(id UUID, name TEXT) AS $$
DECLARE
    v_user RECORD;
BEGIN
    FOR v_user IN SELECT * FROM "user" LOOP
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
    FROM "user" u
    WHERE u.deleted_at IS NULL;
END;
$$ LANGUAGE plpgsql STABLE;

-- ✅ BETTER: Pure SQL (no PL/pgSQL overhead)
CREATE VIEW active_users AS
SELECT id, name FROM "user" WHERE deleted_at IS NULL;
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
    SELECT COUNT(*) INTO v_count FROM "user"; -- v_count not declared!
END $$;

-- ✅ GOOD
DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM "user";
    RAISE NOTICE 'Count: %', v_count;
END $$;
```

---

## Quick Reference

### Session API Summary

pgmi uses a two-tier API: internal tables (`_pgmi_*` prefix) and public views (`*_view` suffix).

| Object | Type | Purpose |
|--------|------|---------|
| `pgmi_plan_view` | VIEW | Files ordered by metadata for direct EXECUTE |
| `pgmi_source_view` | VIEW | All loaded files with path, content, metadata |
| `pgmi_test_source_view` | VIEW | Test files from `__test__/` directories |
| `pgmi_parameter_view` | VIEW | CLI parameters |
| `pgmi_test_plan(pattern)` | FUNCTION | Get test execution plan |
| `pgmi_test_generate(pattern, callback)` | FUNCTION | Generate test SQL with savepoints |
| `CALL pgmi_test()` | MACRO | Preprocessor macro for running tests |
| `current_setting('pgmi.key', true)` | BUILT-IN | Access parameter (use with COALESCE for default) |

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

