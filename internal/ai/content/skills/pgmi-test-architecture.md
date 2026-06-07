---
name: pgmi-test-architecture
description: "Use when organizing test files, __test__/ directories, or planning test strategy in pgmi"
user_invocable: true
---


## Purpose

Deep understanding of pgmi's pure PostgreSQL, transactional test system with zero abstraction, enabling effective test planning and implementation for pgmi projects.

## When to Use

- ✅ When planning tests for pgmi deployments
- ✅ When designing test strategies for pgmi features
- ✅ When organizing test files in pgmi templates
- ✅ When troubleshooting test execution issues
- ✅ When running tests independently of pgmi (power user workflow)
- ❌ For general software testing (use testing-strategy.md instead)

## Core Testing Philosophy

**Pure PostgreSQL, Fail-Fast**:
- pgmi does NOT provide any assertion framework or testing DSL
- Tests are pure SQL/PL/pgSQL that use standard PostgreSQL error handling
- Failed assertions use `RAISE EXCEPTION` - deployment stops immediately
- No custom PASS/FAIL strings, no result tables, no test runners
- Tests succeed silently (or with `RAISE NOTICE` for progress), fail loudly

**Example**:
```sql
-- ❌ BAD: Custom assertion framework
SELECT CASE
    WHEN result = expected THEN 'PASS'
    ELSE 'FAIL'
END AS test_result;

-- ✅ GOOD: Standard PostgreSQL exception
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM migration_script WHERE path = 'test.sql') THEN
        RAISE EXCEPTION 'TEST FAILED: Migration not tracked';
    END IF;
END $$;
```

**Rationale**:
- Users already know PostgreSQL error handling - no learning curve
- Deployment stops immediately on first failure (fail-fast)
- Error messages appear naturally in deployment output
- No framework to maintain, document, or explain
- Works seamlessly with PostgreSQL's transactional semantics

## Test Isolation Model

### 1. Physical Isolation

**Tests reside in `__test__/` directories**:
```
migrations/
  001_create_users.sql
  002_add_roles.sql
  __test__/                    # Isolated from deployment
    _setup.sql                 # Shared fixtures
    test_user_creation.sql
    test_role_assignment.sql
```

**Session Preparation**:
- During session setup, pgmi scans all SQL files
- Test files (from `__test__/` directories) are loaded directly into `_pgmi_test_source` temp table — they never enter `_pgmi_source`
- This prevents accidental test execution during deployment (critical for data safety)
- Deployment files go to `_pgmi_source`; test files go to `_pgmi_test_source`
- Public views (`pgmi_test_source_view`, `pgmi_source_view`) provide stable access

**Why This Matters**:
Test files often manipulate schema and data for testing purposes. If executed as part of a deployment transaction by mistake, these scripts can result in unrecoverable damage to the dataset. Physical isolation ensures the user cannot include these into the deployment execution by mistake.

### 2. Transactional Safety

**ALL tests run inside a single transaction with automatic ROLLBACK**:
```
BEGIN;
    SAVEPOINT test_1;
    -- Execute test 1
    ROLLBACK TO SAVEPOINT test_1;

    SAVEPOINT test_2;
    -- Execute test 2
    ROLLBACK TO SAVEPOINT test_2;
ROLLBACK;
```

**Guarantees**:
- ✅ Tests have ZERO side effects - no schema changes, no data persistence
- ✅ Savepoints provide isolation between individual tests
- ✅ Rollback at end ensures clean state

**Pattern**:
```sql
-- CALL pgmi_test() macro generates this structure
BEGIN;  -- Outer transaction

FOR v_test IN (SELECT * FROM pg_temp.pgmi_test_source_view ORDER BY path)
LOOP
    SAVEPOINT before_test;
    BEGIN
        -- Execute test
        EXECUTE v_test.content;
    EXCEPTION WHEN OTHERS THEN
        -- Test failed, rollback this test
        ROLLBACK TO SAVEPOINT before_test;
        RAISE;  -- Re-raise error (fail-fast)
    END;
    ROLLBACK TO SAVEPOINT before_test;  -- Clean up even on success
END LOOP;

ROLLBACK;  -- Cleanup entire transaction
```

### 3. Test Execution Flow

```
pgmi deploy . -d test_db  (with pgmi_test() in deploy.sql)

├─ File Discovery
│  ├─ Scan project directory for all SQL files
│  └─ Identify __test__/ directories

├─ Session Preparation
│  ├─ Load deployment files into pg_temp._pgmi_source
│  ├─ Load test files into pg_temp._pgmi_test_source
│  ├─ Apply regex filter (if specified)
│  └─ Generate execution plan: pgmi_test_plan(pattern)

├─ Execution Order (per directory with _setup.sql)
│  ├─ SAVEPOINT created before _setup.sql
│  ├─ _setup.sql runs fixtures
│  ├─ Test files in lexicographic order
│  ├─ Subdirectories depth-first (nested SAVEPOINTs)
│  └─ ROLLBACK TO SAVEPOINT (automatic cleanup)

└─ Execute in Transaction
   ├─ BEGIN;
   ├─ SAVEPOINT before each _setup.sql
   ├─ Tests run within savepoint context
   ├─ ROLLBACK TO SAVEPOINT after tests complete
   └─ ROLLBACK; (cleanup entire transaction)
```

## Test Helper Functions

### Available Functions

**`pgmi_test()` Preprocessor Macro** - Execute tests (expanded by Go before sending to PostgreSQL):
```sql
-- Run all tests
CALL pgmi_test();

-- Run only authentication tests (POSIX regex pattern)
CALL pgmi_test('.*/auth/.*');

-- Schema-qualified form also supported
CALL pg_temp.pgmi_test();
```

**Pattern Syntax**: Uses POSIX regular expressions (PostgreSQL `~` operator), NOT SQL LIKE patterns:
- Use `.*` for "match anything" (not `%`)
- Escape literal dots: `\\.` for file extensions
- Anchors: `^` (start), `$` (end)
- Alternatives: `(foo|bar|baz)`
- Character classes: `[a-z0-9_]`

**Parameter Access** - Use PostgreSQL's native `current_setting()`:
```sql
v_env := COALESCE(current_setting('pgmi.env', true), 'test');
```

**`pgmi_test_plan(pattern)`** - Returns test execution plan as a table (for introspection):
```sql
-- See what tests would run
SELECT * FROM pg_temp.pgmi_test_plan();

-- Filter by pattern
SELECT * FROM pg_temp.pgmi_test_plan('.*/auth/.*');
```

Returns: `ordinal`, `step_type` (fixture/test/teardown), `script_path`, `directory`, `depth`

## Test Patterns & Best Practices

### Pattern 1: Simple Assertion Test

```sql
-- __test__/test_email_validation.sql
DO $$
BEGIN
    -- Test valid email
    IF NOT validate_email('user@example.com') THEN
        RAISE EXCEPTION 'TEST FAILED: Valid email rejected';
    END IF;

    -- Test invalid email
    IF validate_email('invalid-email') THEN
        RAISE EXCEPTION 'TEST FAILED: Invalid email accepted';
    END IF;

    RAISE NOTICE '✓ Email validation working correctly';
END $$;
```

### Pattern 2: Functional Test with Setup

**Shared Setup**:
```sql
-- __test__/_setup.sql
-- Runs before all tests in this directory
INSERT INTO test_users VALUES (999, 'fixture@example.com', 'Test User');
INSERT INTO test_roles VALUES (1, 'admin');
```

**Test Using Fixtures**:
```sql
-- __test__/test_user_role_assignment.sql
DO $$
DECLARE
    v_result RECORD;
BEGIN
    -- Act: Assign role to fixture user
    PERFORM assign_role(999, 1);

    -- Assert: Verify assignment
    SELECT * INTO v_result
    FROM user_roles
    WHERE user_id = 999 AND role_id = 1;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: Role not assigned';
    END IF;

    RAISE NOTICE '✓ Role assignment successful';
END $$;
```

### Pattern 3: Parameterized Test

```sql
-- __test__/test_environment_aware.sql
DO $$
DECLARE
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'dev');
BEGIN
    IF v_env = 'production' THEN
        -- Production-specific validation
        IF NOT EXISTS (SELECT 1 FROM security_policies WHERE enabled = true) THEN
            RAISE EXCEPTION 'TEST FAILED: Security policies must be enabled in production';
        END IF;
    END IF;

    RAISE NOTICE '✓ Environment checks passed for: %', v_env;
END $$;
```

### Pattern 4: Regression Test

```sql
-- __test__/test_issue_123_duplicate_emails.sql
-- Regression test for issue #123: Duplicate emails allowed
DO $$
BEGIN
    -- Setup: Insert first user
    INSERT INTO users (email) VALUES ('test@example.com');

    -- Act: Try to insert duplicate email
    BEGIN
        INSERT INTO users (email) VALUES ('test@example.com');
        RAISE EXCEPTION 'TEST FAILED: Duplicate email was allowed (issue #123)';
    EXCEPTION WHEN unique_violation THEN
        -- Expected behavior - unique constraint working
        RAISE NOTICE '✓ Duplicate email correctly rejected';
    END;
END $$;
```

### Pattern 5: Integration Test

```sql
-- __test__/integration/test_user_registration_flow.sql
DO $$
DECLARE
    v_user_id INT;
    v_profile_id INT;
BEGIN
    -- Act: Complete registration flow
    SELECT create_user('newuser@example.com', 'password123') INTO v_user_id;
    SELECT create_profile(v_user_id, 'New User') INTO v_profile_id;
    PERFORM send_welcome_email(v_user_id);

    -- Assert: All components created
    IF NOT EXISTS (SELECT 1 FROM users WHERE id = v_user_id) THEN
        RAISE EXCEPTION 'TEST FAILED: User not created';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM profiles WHERE user_id = v_user_id) THEN
        RAISE EXCEPTION 'TEST FAILED: Profile not created';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM email_queue WHERE user_id = v_user_id) THEN
        RAISE EXCEPTION 'TEST FAILED: Welcome email not queued';
    END IF;

    RAISE NOTICE '✓ User registration flow complete';
END $$;
```

## Test Organization

### Directory Structure Patterns

**Mirror Deployment Structure**:
```
pre-deployment/
  00_extensions.sql
  01_schemas.sql
  __test__/
    test_extensions.sql
    test_schemas.sql

migrations/
  001_create_users.sql
  002_create_roles.sql
  __test__/
    _setup.sql              # Shared fixtures
    test_user_table.sql
    test_role_table.sql
    integration/
      test_user_role_flow.sql

setup/
  api_functions.sql
  __test__/
    test_api_functions.sql
```

**Benefits**:
- Tests live close to code they're testing
- Easy to find related tests
- Clear ownership (team responsible for code owns tests)

### Test Naming Conventions

**File Naming**:
```
test_<feature>.sql          # Feature test
test_<component>_<scenario>.sql  # Specific scenario
test_issue_<number>.sql     # Regression test
_setup.sql                  # Shared fixtures (wrapped in SAVEPOINT)
```

**Descriptive Names**:
```
✅ GOOD:
test_email_validation.sql
test_user_creation_with_profile.sql
test_issue_123_duplicate_emails.sql

❌ BAD:
test1.sql
user_test.sql
temp.sql
```

## Testing Gotchas

### 1. No Template Expansion in Tests

**Problem**: Test files do NOT get template expansion (`${placeholder}` won't work)

**Solution**: Use `current_setting()` for runtime parameter access

```sql
-- ❌ BAD: Template expansion doesn't work in tests
CREATE TABLE ${schema}.users (...);  -- Won't be replaced

-- ✅ GOOD: Runtime parameter access
DO $$
DECLARE
    v_schema TEXT := COALESCE(current_setting('pgmi.schema', true), 'public');
BEGIN
    EXECUTE format('CREATE TABLE %I.users (...)', v_schema);
END $$;
```

### 2. Setup State Persistence

**Behavior**: `_setup.sql` state persists for all tests in same directory

**Implications**:
```
__test__/
  _setup.sql              # Runs once, state persists
  test_a.sql              # Sees setup data
  test_b.sql              # Also sees setup data (not isolated from test_a)
```

**Isolation Strategy**:
- Tests in same directory share setup (not isolated from each other)
- Use separate directories for true isolation
- Or use savepoints within tests for local isolation

### 3. Execution Order

**Tests execute in lexicographic path order (depth-first)**:
```
Execution order:
1. migrations/__test__/_setup.sql
2. migrations/__test__/test_a.sql
3. migrations/__test__/test_b.sql
4. migrations/__test__/integration/test_flow.sql
5. setup/__test__/test_api.sql
```

**Control Order**: Use numeric prefixes if order matters:
```
__test__/
  _setup.sql
  001_test_foundation.sql
  002_test_dependent_feature.sql
```

## pgmi-Independent Testing

pgmi provides a function to persist the test execution plan for external inspection or CI/CD integration.

### Persisting Test Plans

The `pgmi_persist_test_plan` function copies the session-scoped test plan to a permanent schema:

```sql
-- During deployment, persist the test plan to a permanent schema
SELECT pg_temp.pgmi_persist_test_plan('internal', NULL);  -- All tests
SELECT pg_temp.pgmi_persist_test_plan('internal', '/api/');  -- Filtered
```

**Persistent Table Structure (auto-created):**
```sql
-- Created by pgmi_persist_test_plan in target schema
CREATE TABLE <schema>.pgmi_test_plan (
    ordinal INT PRIMARY KEY,
    step_type TEXT NOT NULL,  -- 'fixture', 'test', 'teardown'
    script_path TEXT,         -- NULL for teardown
    directory TEXT NOT NULL,
    depth INT NOT NULL
);
```

### Querying Persisted Test Plans

**Inspect test execution order:**
```bash
psql -d mydb -c "SELECT ordinal, step_type, script_path FROM internal.pgmi_test_plan ORDER BY ordinal;"
```

### Limitations

The persisted test plan contains metadata only (ordinal, step type, paths), not test content. Test execution still requires a pgmi session with loaded source files, or you must separately persist test content if needed for standalone execution.

### Use Cases

- **CI/CD visibility**: Query `pgmi_test_plan` to understand what tests will run
- **Test auditing**: Track which tests were deployed
- **External tooling**: Build custom test runners using the plan metadata

## Test Execution via pgmi_test() Macro

Tests run as part of deployment via the `pgmi_test()` preprocessor macro in deploy.sql.

**Does NOT**:
- ❌ Create persistent test data (savepoints roll back)

**Does**:
- ✅ Execute ONLY test files from `__test__/` directories
- ✅ Run tests within savepoints (test data rolls back, migrations commit)
- ✅ Fail immediately on first test failure (fail-fast)
- ✅ Gate the COMMIT — failed tests mean zero database changes

**Usage in deploy.sql**:
```sql
BEGIN;

-- ... your migrations ...

-- Run all tests
CALL pgmi_test();

COMMIT;
```

### Regex Filtering

Pass a pattern to `CALL pgmi_test()` to filter tests:

```sql
-- Run only auth tests (automatically includes required _setup.sql fixtures)
CALL pgmi_test('.*/auth/.*');
```

## Verification Strategy in Deployments

**Pattern**: Run tests after deployment using the preprocessor macro

```sql
-- deploy.sql
DO $$
DECLARE v_file RECORD;
BEGIN
    -- Phase 1: Migrations
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%' ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

    -- Phase 2: Setup
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './setup/%' ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

END $$;

-- Phase 3: Run tests (preprocessor macro handles savepoint isolation)
CALL pgmi_test();
```

## Template Compliance

### Basic Template

- Simple `__test__/` directory with minimal examples
- Demonstrates basic assertion patterns
- Good for simple projects with linear migrations

### Advanced Template

- Comprehensive test suite with multiple test types
- Uses `_setup.sql` fixtures with automatic SAVEPOINT rollback
- Demonstrates advanced patterns (parameterized tests, regression tests)
- Metadata-driven execution with test validation
- Optional test plan persistence via `pgmi_persist_test_plan()`

## Integration with Other Skills

- **Builds on**: pgmi-philosophy.md (session-centric, SQL-centric)
- **Implements**: testing-strategy.md (test types, patterns)
- **Guided by**: quality-principles.md (test-driven design)
- **Informs**: phased-implementation.md (test each phase)

## Go Integration Tests for Templates

**CRITICAL: Template integration tests MUST use EmbedFileSystem — NEVER real filesystem.**

When testing scaffold templates against a real PostgreSQL database, always use the in-memory path:

```go
// ✅ CORRECT: EmbedFileSystem → Deployer → PostgreSQL (no disk I/O)
embedFS := scaffold.GetTemplatesFS()
efs := filesystem.NewEmbedFileSystem(embedFS, "templates/"+templateName)
deployer := testhelpers.NewTestDeployerWithFS(t, efs)
err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
    SourcePath: ".", // EmbedFileSystem root = template root
    // ...
})
```

```go
// ❌ WRONG: Real filesystem, CLI subprocess, temp directories
scaffolder.CreateProject("test_project", templateName, t.TempDir())
cmd := exec.Command("pgmi", "deploy", tmpDir, ...)
```

**Why:**
- No temp files to leak or clean up
- No filesystem permission issues
- No stale files left on disk after test failures
- Faster execution (no disk I/O)
- Deterministic (embedded content is immutable at compile time)

**Reference implementation:** `internal/scaffold/integration_test.go`

## Common Pitfalls

- ❌ **Creating Test Framework**: Building custom assertion DSL
- ✅ **Use PostgreSQL Native**: RAISE EXCEPTION for failures

- ❌ **Persistent Test Data**: Leaving test data in database
- ✅ **Transactional Tests**: Automatic rollback ensures cleanup

- ❌ **Testing in Production DB**: Running tests against live data
- ✅ **Dedicated Test DB**: Always use separate test database

- ❌ **Ignoring Test Failures**: Continuing despite errors
- ✅ **Fail-Fast**: First failure stops execution immediately

- ❌ **Real filesystem for template tests**: TempDir, os.WriteFile, exec.Command
- ✅ **EmbedFileSystem**: In-memory template reading via NewTestDeployerWithFS()

## Examples

### Example: Complete Test Suite for User Module

```
users/
  migrations/
    001_create_users_table.sql
    002_add_email_index.sql
    __test__/
      _setup.sql                    # Shared fixtures
      test_users_table_schema.sql   # Schema validation
      test_email_uniqueness.sql     # Constraint test
      integration/
        test_user_crud_flow.sql     # E2E test

  setup/
    user_functions.sql
    __test__/
      test_create_user.sql
      test_validate_email.sql
      test_issue_45_empty_email.sql  # Regression test
```

**_setup.sql**:
```sql
-- Create test fixtures
INSERT INTO users (id, email, name) VALUES
    (9990, 'test1@example.com', 'Test User 1'),
    (9991, 'test2@example.com', 'Test User 2');
```

**test_email_uniqueness.sql**:
```sql
DO $$
BEGIN
    -- Should reject duplicate email
    BEGIN
        INSERT INTO users (email, name) VALUES ('test1@example.com', 'Duplicate');
        RAISE EXCEPTION 'TEST FAILED: Duplicate email accepted';
    EXCEPTION WHEN unique_violation THEN
        RAISE NOTICE '✓ Duplicate email correctly rejected';
    END;
END $$;
```

**Run Tests**:
Tests are executed via the `CALL pgmi_test()` macro in deploy.sql:
```sql
-- In deploy.sql, call the pgmi_test() preprocessor macro
CALL pgmi_test('./users/**');
```

Then deploy:
```bash
pgmi deploy ./users -d test_db --overwrite --force
```

