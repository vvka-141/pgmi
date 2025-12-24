# Testing Guide

This document explains how to write and run tests for pgmi using Go-native testing practices.

## Test Organization

pgmi follows a layered testing approach:

1. **Pure Unit Tests** - Fast, no external dependencies
2. **Service Integration Tests** - Test Go interfaces with real database
3. **Template/Scaffold Tests** - Validate complete workflows

## Running Tests

### Unit Tests Only (Fast)
```bash
go test -short ./...
```

This runs tests that don't require a database. Perfect for rapid local development and CI pre-merge checks.

### All Tests (Including Database Integration)
```bash
# Configure test database connection
export PGMI_TEST_CONN="postgresql://postgres:postgres@localhost:5433/postgres"

# Run all tests
go test ./...

# Run with verbose output
go test -v ./...
```

### Specific Test Packages
```bash
# Run only service integration tests
go test ./internal/services

# Run only scaffold template tests
go test ./internal/scaffold

# Run specific test
go test -run TestDeploymentService_Deploy_BasicWorkflow ./internal/services
```

## Writing Tests

### Pure Unit Tests

For components with no external dependencies (parsers, validators, calculators):

```go
package mypackage

import "testing"

func TestMyFunction(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {"case1", "input1", "output1"},
        {"case2", "input2", "output2"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := MyFunction(tt.input)
            if result != tt.expected {
                t.Errorf("got %v, want %v", result, tt.expected)
            }
        })
    }
}
```

### Service Integration Tests (Using Deployer Interface)

For testing deployment logic with a real database:

```go
package mypackage_test

import (
    "context"
    "testing"

    testhelpers "github.com/pgmi/pgmi/internal/testing"
    "github.com/pgmi/pgmi/pkg/pgmi"
)

func TestMyDeploymentScenario(t *testing.T) {
    // Skip if no database or running in short mode
    connString := testhelpers.RequireDatabase(t)

    ctx := context.Background()

    // Create a Deployer instance for testing
    deployer := testhelpers.NewTestDeployer(t)

    // Create test project
    projectPath := t.TempDir()
    createTestProject(t, projectPath)

    testDB := "my_test_db"
    defer testhelpers.CleanupTestDB(t, connString, testDB)

    // Deploy using the Deployer interface
    err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
        ConnectionString: connString,
        DatabaseName:     testDB,
        SourcePath:       projectPath,
        Overwrite:        true,
        Force:            true,
        Verbose:          testing.Verbose(),
    })

    if err != nil {
        t.Fatalf("Deploy failed: %v", err)
    }

    // Verify deployment
    pool := testhelpers.GetTestPool(t, connString, testDB)

    var result int
    err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM my_table").Scan(&result)
    if err != nil {
        t.Fatalf("Query failed: %v", err)
    }

    if result != 42 {
        t.Errorf("Expected 42 rows, got %d", result)
    }
}

func createTestProject(t *testing.T, projectPath string) {
    // Helper to create test SQL files
    // See internal/services/deployer_integration_test.go for examples
}
```

### Database Test Helpers

The `internal/testing` package provides helpers for database integration tests:

#### `RequireDatabase(t)`
Gets test connection string and skips test if unavailable or in short mode:
```go
connString := testhelpers.RequireDatabase(t)
```

#### `NewTestDeployer(t)`
Creates a configured `Deployer` instance for testing:
```go
deployer := testhelpers.NewTestDeployer(t)
err := deployer.Deploy(ctx, config)
```

#### `CleanupTestDB(t, connString, dbName)`
Drops test database with proper connection termination:
```go
testDB := "my_test_db"
defer testhelpers.CleanupTestDB(t, connString, testDB)
```

#### `GetTestPool(t, connString, dbName)`
Gets connection pool to test database (auto-closed on test completion):
```go
pool := testhelpers.GetTestPool(t, connString, testDB)
var count int
pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
```

## Test Database Setup

### Local PostgreSQL (Docker)
```bash
docker run -d \
  --name pgmi-test \
  -e POSTGRES_PASSWORD=postgres \
  -p 5433:5432 \
  postgres:16

export PGMI_TEST_CONN="postgresql://postgres:postgres@localhost:5433/postgres"
```

### Verify Connection
```bash
psql $PGMI_TEST_CONN -c "SELECT version();"
```

## CI/CD Integration

### GitHub Actions Example
```yaml
name: Tests
on: [push, pull_request]

jobs:
  unit-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - name: Run unit tests
        run: go test -short ./...

  integration-tests:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_PASSWORD: postgres
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
        ports:
          - 5432:5432
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - name: Run integration tests
        env:
          PGMI_TEST_CONN: postgresql://postgres:postgres@localhost:5432/postgres
        run: go test ./...
```

## Best Practices

### 1. Use -short Flag Appropriately
```go
func TestMyIntegrationTest(t *testing.T) {
    connString := testhelpers.RequireDatabase(t) // auto-skips if short mode
    // test code...
}
```

### 2. Always Clean Up Resources
```go
testDB := "my_test_db"
defer testhelpers.CleanupTestDB(t, connString, testDB)
```

### 3. Test via Public Interfaces
```go
// Good: Test via Deployer interface
deployer := testhelpers.NewTestDeployer(t)
err := deployer.Deploy(ctx, config)

// Bad: Test via CLI (slower, less control)
cmd := exec.Command("pgmi", "deploy", ...)
```

### 4. Use Table-Driven Tests
```go
tests := []struct {
    name     string
    config   pgmi.DeploymentConfig
    wantErr  bool
    errMsg   string
}{
    {"valid deployment", validConfig, false, ""},
    {"missing deploy.sql", invalidConfig, true, "deploy.sql not found"},
}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        // test logic
    })
}
```

### 5. Use t.Helper() in Test Utilities
```go
func createTestProject(t *testing.T, path string) {
    t.Helper() // Makes error messages point to caller
    // setup code...
}
```

## Troubleshooting

### Tests Hang or Timeout
- Check database connections are properly closed
- Verify no zombie transactions holding locks
- Ensure `defer pool.Close()` or use test helpers

### "PGMI_TEST_CONN not set"
```bash
export PGMI_TEST_CONN="postgresql://postgres:postgres@localhost:5433/postgres"
```

### "Database already exists" Errors
Test cleanup may have failed. Manually drop:
```bash
psql $PGMI_TEST_CONN -c "DROP DATABASE IF EXISTS pgmi_test_basic;"
```

## Examples

See these files for comprehensive examples:
- [internal/services/deployer_integration_test.go](internal/services/deployer_integration_test.go) - Service integration tests
- [internal/scaffold/integration_test.go](internal/scaffold/integration_test.go) - Template deployment tests
- [internal/db/parser_test.go](internal/db/parser_test.go) - Pure unit tests
- [internal/testing/dbhelper.go](internal/testing/dbhelper.go) - Test helper implementations
