---
name: pgmi-testing-review
description: "Use when reviewing tests in SQL or Go"
user_invocable: true
---


**Purpose**: Cross-cutting testing expertise for all pgmi code (SQL, HTTP, Go). Ensures comprehensive test coverage, proper organization, and effective testing patterns.

**Used By**:
- postgres-sql-reviewer (PostgreSQL fail-fast testing)
- http-expert-reviewer (HTTP endpoint testing)
- golang-expert-reviewer (Go testing patterns)
- general-purpose (when writing testable code)
- change-planner (planning test strategies)

**Depends On**: pgmi-review-philosophy, pgmi-sql (PostgreSQL testing - if exists), pgmi-golang-review (Go testing)

**Auto-Load With**:
- `pgmi-sql` skill (PostgreSQL testing)
- `pgmi-templates` skill (template testing)
- File patterns: `**/__test__/*.sql`, `**/*_test.go`
- Keywords: "test", "coverage", "assertion", "mock"

**Load For**: Test-driven development, test organization, coverage expectations

---

## pgmi Testing Philosophy

### Pure PostgreSQL, Fail-Fast

**Core Principle**: pgmi provides NO custom testing framework. Tests use standard PostgreSQL `RAISE EXCEPTION` to fail.

```sql
-- ❌ BAD: Custom test framework
CREATE FUNCTION test_migration() RETURNS TEXT AS $$
BEGIN
    IF (SELECT COUNT(*) FROM migration_script) > 0 THEN
        RETURN 'PASS';
    ELSE
        RETURN 'FAIL: No migrations found';
    END IF;
END;
$$ LANGUAGE plpgsql;

-- ✅ GOOD: Standard PostgreSQL exception
CREATE FUNCTION test_migration() RETURNS VOID AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM migration_script) THEN
        RAISE EXCEPTION 'TEST FAILED: No migrations found';
    END IF;
    -- Test passes silently (or with RAISE NOTICE for progress)
END;
$$ LANGUAGE plpgsql;
```

**Rationale**:
- Users already know PostgreSQL error handling
- Deployment stops immediately on first failure (fail-fast)
- Error messages appear naturally in output
- No framework to learn, maintain, or document
- Works seamlessly with PostgreSQL transactions

---

## Test Organization

### PostgreSQL Test Structure

**Inline Tests** (Pure Functions):
```sql
-- Function definition
CREATE FUNCTION public.calculate_total(p_quantity INT, p_price NUMERIC)
RETURNS NUMERIC AS $$
    SELECT p_quantity * p_price;
$$ LANGUAGE SQL IMMUTABLE;

-- Inline test (right after function)
DO $$
BEGIN
    IF public.calculate_total(5, 10.00) != 50.00 THEN
        RAISE EXCEPTION 'TEST FAILED: calculate_total(5, 10) should return 50';
    END IF;
    RAISE NOTICE 'PASS: calculate_total';
END $$;
```

**`__test__/` Tests** (Transactional Tests):
```
migrations/
  __test__/
    _setup.sql            # Shared fixtures (wrapped in SAVEPOINT)
    test_migrations.sql
    test_rollback.sql
  001_create_users.sql
  002_add_email_index.sql
```

**Rule**: Inline tests for pure functions (no side effects). `__test__/` for transactional tests (schema changes, data queries).

**Automatic Cleanup**: pgmi creates a SAVEPOINT before `_setup.sql` and automatically rolls back after tests—no teardown files needed.

**Review Checklist**:
- [ ] Pure functions tested inline?
- [ ] Transactional tests in `__test__/` directories?
- [ ] `_setup.sql` file provides shared fixtures?

### Go Test Structure

```
internal/
  services/
    deployer.go
    deployer_test.go                    # Unit tests
    deployer_integration_test.go        # Integration tests (same package)
    session_integration_test.go
  files/
    loader/
      loader.go
      loader_test.go                    # Unit tests
      loader_integration_test.go        # Integration tests
  scaffold/
    scaffold.go
    scaffold_test.go                    # Unit tests
    testhelpers/
      deployer.go                       # Test helpers for deployer
```

**Naming Convention**:
- `*_test.go` - Unit tests (no database required)
- `*_integration_test.go` - Integration tests (require PostgreSQL)

**Review Checklist**:
- [ ] Test files colocated with implementation (`*_test.go`)?
- [ ] Integration tests use `*_integration_test.go` suffix?
- [ ] Test helpers in `testhelpers/` subdirectory when needed?
- [ ] No test code in `internal/` imported by non-test code?

---

## PostgreSQL Testing Patterns

### Fail-Fast with RAISE EXCEPTION

```sql
-- ✅ GOOD: Test fails immediately with clear message
DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM migration_script;

    IF v_count = 0 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected at least one migration, found 0';
    END IF;

    RAISE NOTICE 'PASS: Found % migrations', v_count;
END $$;
```

### Transactional Isolation

**Pattern**: Tests run in transaction, automatically rollback.

```sql
-- Test setup
BEGIN;
    -- Create test data
    INSERT INTO users (id, name, email)
    VALUES (gen_random_uuid(), 'Test User', 'test@example.com');

    -- Test logic
    DO $$
    DECLARE
        v_user_count INT;
    BEGIN
        SELECT COUNT(*) INTO v_user_count FROM users WHERE name = 'Test User';

        IF v_user_count != 1 THEN
            RAISE EXCEPTION 'TEST FAILED: Expected 1 test user, found %', v_user_count;
        END IF;

        RAISE NOTICE 'PASS: User creation test';
    END $$;
ROLLBACK; -- Cleanup automatic
```

**CALL pgmi_test() Macro**: Executes tests within savepoints with automatic rollback.
```sql
-- In deploy.sql
CALL pgmi_test();
-- All tests run within savepoints, test data rolled back after execution
```

### Test Data Management

**Pattern**: Setup fixtures in `_setup.sql`, use in tests.

```sql
-- __test__/_setup.sql
CREATE TEMP TABLE test_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE
);

INSERT INTO test_users (name, email) VALUES
    ('Alice', 'alice@example.com'),
    ('Bob', 'bob@example.com'),
    ('Charlie', 'charlie@example.com');

-- Create helper function
CREATE FUNCTION test_get_user_count() RETURNS INT AS $$
    SELECT COUNT(*)::INT FROM test_users;
$$ LANGUAGE SQL STABLE;
```

```sql
-- __test__/01_test_user_operations.sql
DO $$
BEGIN
    IF test_get_user_count() != 3 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected 3 test users';
    END IF;

    RAISE NOTICE 'PASS: User count test';
END $$;
```

### Testing Idempotency

```sql
-- Test that migration can run twice safely
DO $$
DECLARE
    v_result1 INT;
    v_result2 INT;
BEGIN
    -- First execution
    v_result1 := public.execute_migration_script('migrations/001_create_users.sql');

    -- Second execution (should be idempotent)
    v_result2 := public.execute_migration_script('migrations/001_create_users.sql');

    IF v_result2 != -1 THEN
        RAISE EXCEPTION 'TEST FAILED: Re-execution should return -1 (skipped), got %', v_result2;
    END IF;

    RAISE NOTICE 'PASS: Idempotency test';
END $$;
```

### Testing Error Conditions

```sql
-- Test that invalid input raises exception
DO $$
BEGIN
    -- This should fail
    PERFORM public.execute_migration_script('nonexistent.sql');

    -- If we reach here, test failed
    RAISE EXCEPTION 'TEST FAILED: Expected exception for nonexistent file';
EXCEPTION
    WHEN OTHERS THEN
        -- Expected exception, test passes
        RAISE NOTICE 'PASS: Error handling test';
END $$;
```

**Review Checklist**:
- [ ] Tests use `RAISE EXCEPTION` for failures?
- [ ] Tests pass silently or with `RAISE NOTICE`?
- [ ] Test data isolated (temp tables, transactional rollback)?
- [ ] Idempotency tested for migrations?
- [ ] Error conditions tested with EXCEPTION blocks?

---

## Go Testing Patterns

### Table-Driven Tests

```go
func TestParseConnectionString(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    *ConnectionConfig
        wantErr bool
    }{
        {
            name:  "valid PostgreSQL URI",
            input: "postgresql://user:pass@localhost:5432/dbname",
            want: &ConnectionConfig{
                Host:     "localhost",
                Port:     5432,
                Database: "dbname",
                Username: "user",
                Password: "pass",
            },
            wantErr: false,
        },
        {
            name:    "invalid URI",
            input:   "not-a-uri",
            want:    nil,
            wantErr: true,
        },
        {
            name:  "URI with SSL mode",
            input: "postgresql://user:pass@localhost:5432/dbname?sslmode=require",
            want: &ConnectionConfig{
                Host:     "localhost",
                Port:     5432,
                Database: "dbname",
                Username: "user",
                Password: "pass",
                SSLMode:  "require",
            },
            wantErr: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ParseConnectionString(tt.input)

            if (err != nil) != tt.wantErr {
                t.Errorf("ParseConnectionString() error = %v, wantErr %v", err, tt.wantErr)
                return
            }

            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("ParseConnectionString() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

**Benefits**:
- ✅ Multiple test cases with single test function
- ✅ Easy to add new cases
- ✅ Clear failure messages (test name + input/output)

### Subtests for Logical Grouping

```go
func TestDeployer(t *testing.T) {
    t.Run("successful deployment", func(t *testing.T) {
        deployer := NewStandardDeployer(mockConnector, forceApprover)
        err := deployer.Deploy(ctx, config)
        if err != nil {
            t.Errorf("unexpected error: %v", err)
        }
    })

    t.Run("user cancels approval", func(t *testing.T) {
        deployer := NewStandardDeployer(mockConnector, rejectApprover)
        err := deployer.Deploy(ctx, config)
        if err == nil {
            t.Error("expected error when approval rejected")
        }
    })

    t.Run("database connection failure", func(t *testing.T) {
        deployer := NewStandardDeployer(failingConnector, forceApprover)
        err := deployer.Deploy(ctx, config)
        if err == nil {
            t.Error("expected error when connection fails")
        }
    })
}
```

**Benefits**:
- ✅ Logical grouping of related tests
- ✅ Parallel execution (`t.Parallel()` in each subtest)
- ✅ Clear test hierarchy in output

### Test Helpers

Test helpers in pgmi are colocated in `internal/testing/` or as `*_test.go` files within packages.

```go
// internal/testing/helpers.go (pattern example)
package testing

import (
    "context"
    "testing"
    "github.com/jackc/pgx/v5"
)

// MustConnect connects or fails the test
func MustConnect(t *testing.T, connStr string) *pgx.Conn {
    t.Helper() // Marks this as helper (errors report caller line)

    conn, err := pgx.Connect(context.Background(), connStr)
    if err != nil {
        t.Fatalf("failed to connect: %v", err)
    }

    return conn
}

// CreateTestDatabase creates a test database with automatic cleanup
func CreateTestDatabase(t *testing.T, name string) string {
    t.Helper()

    conn := MustConnect(t, "postgresql://postgres@localhost/postgres")
    defer conn.Close(context.Background())

    _, err := conn.Exec(context.Background(), "CREATE DATABASE "+name)
    if err != nil {
        t.Fatalf("failed to create test database: %v", err)
    }

    // Cleanup on test completion
    t.Cleanup(func() {
        conn := MustConnect(t, "postgresql://postgres@localhost/postgres")
        defer conn.Close(context.Background())
        conn.Exec(context.Background(), "DROP DATABASE "+name)
    })

    return "postgresql://postgres@localhost/" + name
}
```

**Usage**:
```go
func TestDeploy(t *testing.T) {
    connStr := createTestDatabase(t, "test_deploy")
    conn := mustConnect(t, connStr)
    defer conn.Close(context.Background())

    // Test logic
}
```

**Review Checklist**:
- [ ] Test helpers use `t.Helper()`?
- [ ] Cleanup registered with `t.Cleanup()`?
- [ ] Helpers return error or call `t.Fatal()` (not both)?

### Mock Design

```go
// Mock for Connector interface
type MockConnector struct {
    ConnectFunc func(ctx context.Context) (*pgx.Conn, error)
}

func (m *MockConnector) Connect(ctx context.Context) (*pgx.Conn, error) {
    if m.ConnectFunc != nil {
        return m.ConnectFunc(ctx)
    }
    return nil, errors.New("ConnectFunc not set")
}

// Usage in test
func TestDeployerWithFailingConnection(t *testing.T) {
    mockConn := &MockConnector{
        ConnectFunc: func(ctx context.Context) (*pgx.Conn, error) {
            return nil, errors.New("connection refused")
        },
    }

    deployer := NewStandardDeployer(mockConn, &ForceApprover{})
    err := deployer.Deploy(context.Background(), &DeployConfig{})

    if err == nil {
        t.Error("expected error when connection fails")
    }
}
```

**Review Checklist**:
- [ ] Mocks implement interfaces (not concrete types)?
- [ ] Mock functions configurable per test?
- [ ] Default behavior defined (error or panic)?

---

## Integration Testing

### PostgreSQL Integration Tests

**Pattern**: Test against real PostgreSQL instance. Integration tests use `*_integration_test.go` suffix and are colocated with unit tests.

```go
// internal/services/deployer_integration_test.go
package services

import (
    "context"
    "testing"
)

func TestDeployBasicTemplate(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }

    // Use testcontainer or PGMI_TEST_CONN environment variable
    connStr := getTestConnectionString(t)

    // Deploy using the actual deployer
    deployer := NewStandardDeployer(/* ... */)
    err := deployer.Deploy(context.Background(), &DeployConfig{
        Path:         "../../internal/scaffold/templates/basic",
        DatabaseName: "test_deploy_basic",
    })

    if err != nil {
        t.Fatalf("deployment failed: %v", err)
    }

    // Verify deployment with direct query
    conn := mustConnect(t, connStr)
    defer conn.Close(context.Background())

    var tableExists bool
    err = conn.QueryRow(context.Background(),
        "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')",
    ).Scan(&tableExists)

    if err != nil {
        t.Fatalf("verification query failed: %v", err)
    }

    if !tableExists {
        t.Error("users table not created")
    }
}
```

**Review Checklist**:
- [ ] Integration tests skipped in short mode (`testing.Short()`)?
- [ ] Test database created and cleaned up?
- [ ] Real PostgreSQL instance used (not mock)?
- [ ] Deployment verified with queries?

### HTTP Integration Tests

```sql
-- __test__/test_http_routes.sql
DO $$
DECLARE
    v_response JSON;
BEGIN
    -- Test GET /api/users/:id
    v_response := api.get_user('550e8400-e29b-41d4-a716-446655440000'::UUID);

    IF (v_response->>'status')::INT != 200 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected 200 OK, got %', v_response->>'status';
    END IF;

    RAISE NOTICE 'PASS: GET /api/users/:id';

    -- Test POST /api/users
    v_response := api.create_user('test@example.com', 'Test User');

    IF (v_response->>'status')::INT != 201 THEN
        RAISE EXCEPTION 'TEST FAILED: Expected 201 Created, got %', v_response->>'status';
    END IF;

    RAISE NOTICE 'PASS: POST /api/users';
END $$;
```

---

## Test Coverage Expectations

### Critical Path Coverage

**Must Cover**:
- [ ] Happy path (successful flow)
- [ ] Error conditions (failures, exceptions)
- [ ] Edge cases (empty input, null values, boundary values)
- [ ] Idempotency (can it run twice safely?)

**Example**:
```go
func TestExecuteMigration(t *testing.T) {
    t.Run("successful execution", func(t *testing.T) {
        // Happy path
    })

    t.Run("file not found", func(t *testing.T) {
        // Error condition
    })

    t.Run("empty file", func(t *testing.T) {
        // Edge case
    })

    t.Run("re-execution (idempotency)", func(t *testing.T) {
        // Idempotency
    })
}
```

### Coverage Metrics

**Go Coverage**:
```bash
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

**Target**: >80% coverage for critical paths (core business logic).

**Not Worth Testing** (100% coverage is overkill):
- Trivial getters/setters
- Simple constructors
- Generated code
- Third-party library wrappers (integration tests instead)

**Review Checklist**:
- [ ] Critical business logic covered?
- [ ] Error paths tested?
- [ ] Edge cases included?
- [ ] Coverage >80% for core packages?

---

## Test Quality Checklist

### PostgreSQL Tests
- [ ] Tests use `RAISE EXCEPTION` for failures?
- [ ] Tests pass silently or with `RAISE NOTICE`?
- [ ] Inline tests for pure functions?
- [ ] `__test__/` for transactional tests?
- [ ] Test data isolated (temp tables, transactions)?
- [ ] Idempotency tested?
- [ ] Error conditions tested?

### Go Tests
- [ ] Table-driven tests for multiple cases?
- [ ] Subtests for logical grouping?
- [ ] Test helpers use `t.Helper()`?
- [ ] Cleanup with `t.Cleanup()`?
- [ ] Mocks implement interfaces?
- [ ] Integration tests skip in short mode?

### Coverage
- [ ] Happy path covered?
- [ ] Error conditions covered?
- [ ] Edge cases covered?
- [ ] Critical path coverage >80%?

### Test Organization
- [ ] Tests colocated with implementation (`*_test.go`)?
- [ ] Integration tests use `*_integration_test.go` suffix?
- [ ] Shared test infrastructure in `internal/testing/`?
- [ ] Test naming clear and descriptive?

---

**End of pgmi-testing-review**

