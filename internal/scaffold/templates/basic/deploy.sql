-- ============================================================================
-- deploy.sql - Deployment Orchestrator
-- ============================================================================
-- This file controls HOW your SQL gets executed. pgmi loads your files
-- into session-scoped temporary tables and lets YOU decide execution order.
--
-- Available session tables:
--   pg_temp.pgmi_source           - Your SQL files (migrations, schemas, etc.)
--   pg_temp.pgmi_unittest_plan    - Your test execution plan (ordered by execution_order)
--   pg_temp.pgmi_parameter        - CLI params from --param key=value
--   pg_temp.pgmi_plan             - Execution plan (populated by helpers below)
--
-- Helper functions:
--   pgmi_declare_param(key, type, ...)         - Declare parameter with type validation and defaults
--   pgmi_get_param(key, default)               - Get parameter value with fallback
--   pgmi_plan_command(sql)                     - Add raw SQL to execution plan
--   pgmi_plan_notice(msg, args...)             - Add log message to plan
--   pgmi_plan_file(path)                       - Add file content to plan
--   pgmi_plan_do(plpgsql_code)                 - Add PL/pgSQL block to plan
--   pgmi_plan_tests(pattern)                   - Execute unit tests with optional path filtering
-- ============================================================================

DO $$
BEGIN
    IF current_setting('server_version_num')::int < 160000 THEN
        RAISE WARNING $msg$
════════════════════════════════════════════════════════════════════════
  PostgreSQL % detected. This template targets PostgreSQL 16+.
  Some SQL features may not be available on your server version.
  If deployment fails, review the error and adjust the SQL to match
  your PostgreSQL version. See: https://www.postgresql.org/docs/release/
════════════════════════════════════════════════════════════════════════
$msg$, current_setting('server_version');
    END IF;
END $$;

-- STEP 1: Declare parameters with defaults and type validation
-- Parameters are automatically available as PostgreSQL session variables
-- with the 'pgmi.' namespace prefix (no manual initialization needed).
--
-- Access methods:
--   current_setting('pgmi.key', true)   - Direct PostgreSQL function (returns NULL if not set)
--   pgmi_get_param('key', 'default')    - Convenience wrapper with default fallback
--
-- Default value is 'World', but you can override with: --param name=Alice
-- Parameters are immediately accessible at runtime:
--   SELECT current_setting('pgmi.name');  -- Returns: 'World' (or 'Alice' if overridden)
--   SELECT pgmi_get_param('name');        -- Convenience wrapper
SELECT pg_temp.pgmi_declare_param(
    p_key => 'name',
    p_default_value => 'World',
    p_description => 'Name to greet in hello_world function'
);

-- STEP 2: Build execution plan (single transaction with test rollback)
DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- ========================================================================
    -- Single Transaction: Migrations + Tests
    -- ========================================================================
    -- Professional PostgreSQL pattern: Deploy everything in one transaction,
    -- use savepoints to test without persisting test artifacts.

    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- ========================================================================
    -- PHASE 1: Migrations (schema, functions, tables)
    -- ========================================================================
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    )
    LOOP
        PERFORM pg_temp.pgmi_plan_notice('Executing: %s', v_file.path);
        PERFORM pg_temp.pgmi_plan_command(v_file.content);
    END LOOP;

    PERFORM pg_temp.pgmi_plan_notice('✓ Migrations complete');

    -- ========================================================================
    -- PHASE 2: Unit Tests (validate deployment without persistence)
    -- ========================================================================
    -- Tests run inside an explicit savepoint and get rolled back.
    -- This ensures absolutely no side effects from test execution.
    --
    -- pgmi_plan_tests() automatically:
    --   - Creates SAVEPOINT before each _setup.sql (fixtures)
    --   - Executes tests in correct order
    --   - Rolls back to SAVEPOINT after tests (automatic cleanup)
    --   - Includes only ancestor _setup.sql files needed for matching tests
    --
    -- Pattern Syntax: POSIX regular expressions (PostgreSQL ~ operator)
    --   • Use .* for "match anything" (not % like SQL LIKE)
    --   • Escape literal dots: \. for file extensions
    --
    -- Examples:
    --   pgmi_plan_tests()                           - Run all tests
    --   pgmi_plan_tests('.*/critical/.*')           - Tests in /critical/ directories
    --   pgmi_plan_tests('.*_(integration|e2e)\.sql$')  - Integration/E2E tests only

    PERFORM pg_temp.pgmi_plan_command('SAVEPOINT before_tests;');
    PERFORM pg_temp.pgmi_plan_tests();  -- Run all tests
    PERFORM pg_temp.pgmi_plan_command('ROLLBACK TO SAVEPOINT before_tests;');
    PERFORM pg_temp.pgmi_plan_notice('✓ All tests passed (no side effects)');

    -- Commit migrations (tests are gone, migrations stay)
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
    PERFORM pg_temp.pgmi_plan_notice('✓ Deployment complete!');
END $$;
