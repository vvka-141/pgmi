package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestDeploymentService_DirectMode_PassingTests verifies that pgmi_test() macro
// executes tests inline during deploy.sql and deployment succeeds when all tests pass.
func TestDeploymentService_DirectMode_PassingTests(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createDirectModeProjectPassing(t, projectPath)

	testDB := "pgmi_direct_mode_pass"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
		Verbose:             testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy with passing tests failed: %v", err)
	}

	// Verify deployment artifacts exist (migrations ran)
	pool := testhelpers.GetTestPool(t, connString, testDB)

	var tableExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'users'
		)
	`).Scan(&tableExists)
	if err != nil {
		t.Fatalf("Failed to check table existence: %v", err)
	}
	if !tableExists {
		t.Error("Expected 'users' table to exist after deployment")
	}
}

// TestDeploymentService_DirectMode_FailingTests verifies that pgmi_test() macro
// causes deployment to fail when a test raises an exception.
func TestDeploymentService_DirectMode_FailingTests(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createDirectModeProjectFailing(t, projectPath)

	testDB := "pgmi_direct_mode_fail"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
		Verbose:             testing.Verbose(),
	})

	if err == nil {
		t.Fatal("Expected deployment to fail due to failing test, but it succeeded")
	}

	// Error should mention test failure
	if !strings.Contains(err.Error(), "Intentional test failure") {
		t.Errorf("Expected error to contain 'Intentional test failure', got: %v", err)
	}
}

// TestDeploymentService_DirectMode_FilterPattern verifies that pgmi_test('./path/**')
// only executes tests matching the specified glob pattern.
func TestDeploymentService_DirectMode_FilterPattern(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createDirectModeProjectWithFilter(t, projectPath)

	testDB := "pgmi_direct_mode_filter"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy - should succeed because the failing test is filtered out
	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
		Verbose:             testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy with filtered tests failed: %v", err)
	}
}

// TestDeploymentService_DirectMode_SavepointRollback verifies that tests execute
// within savepoints and don't persist test data.
func TestDeploymentService_DirectMode_SavepointRollback(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createDirectModeProjectSavepointTest(t, projectPath)

	testDB := "pgmi_direct_mode_savepoint"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
		Verbose:             testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Verify test data was NOT persisted (savepoint rolled back)
	pool := testhelpers.GetTestPool(t, connString, testDB)

	var count int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query users table: %v", err)
	}

	// Tests insert data but savepoint should roll it back
	if count != 0 {
		t.Errorf("Expected 0 users (test data should be rolled back), got %d", count)
	}
}

// TestDeploymentService_DirectMode_FixtureExecution verifies that fixture files
// (00_*.sql or *_fixture.sql) are executed before tests in the same directory.
func TestDeploymentService_DirectMode_FixtureExecution(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createDirectModeProjectWithFixture(t, projectPath)

	testDB := "pgmi_direct_mode_fixture"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
		Verbose:             testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy with fixtures failed: %v", err)
	}
}

// TestDeploymentService_DirectMode_NoTests verifies that pgmi_test() with no
// matching tests doesn't cause errors.
func TestDeploymentService_DirectMode_NoTests(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createDirectModeProjectNoTests(t, projectPath)

	testDB := "pgmi_direct_mode_no_tests"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
		Verbose:             testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy with no tests failed: %v", err)
	}
}

// Helper functions to create test projects

func createDirectModeProjectPassing(t *testing.T, projectPath string) {
	t.Helper()

	// Create migrations directory and migration file
	migrationsPath := filepath.Join(projectPath, "migrations")
	if err := os.MkdirAll(migrationsPath, 0755); err != nil {
		t.Fatalf("Failed to create migrations directory: %v", err)
	}

	migration := `CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`
	if err := os.WriteFile(filepath.Join(migrationsPath, "001_users.sql"), []byte(migration), 0644); err != nil {
		t.Fatalf("Failed to create migration: %v", err)
	}

	// Create __test__ directory with passing test
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'users') THEN
        RAISE EXCEPTION 'users table not found';
    END IF;
    RAISE NOTICE '✓ users table exists';
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_users_exist.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql with pgmi_test() macro
	deploySQL := `
-- Execute migrations first
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN
        SELECT content
        FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Execute tests using direct mode macro
SELECT pgmi_test();
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createDirectModeProjectFailing(t *testing.T, projectPath string) {
	t.Helper()

	// Create __test__ directory with failing test
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `
DO $$
BEGIN
    RAISE EXCEPTION 'Intentional test failure';
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_fail.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql with pgmi_test() macro
	deploySQL := `
-- Execute tests using direct mode macro (should fail)
SELECT pgmi_test();
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createDirectModeProjectWithFilter(t *testing.T, projectPath string) {
	t.Helper()

	// Create __test__/passing directory with passing test
	passingPath := filepath.Join(projectPath, "__test__", "passing")
	if err := os.MkdirAll(passingPath, 0755); err != nil {
		t.Fatalf("Failed to create passing test directory: %v", err)
	}

	passingTest := `DO $$ BEGIN RAISE NOTICE '✓ Passing test'; END $$;`
	if err := os.WriteFile(filepath.Join(passingPath, "test_pass.sql"), []byte(passingTest), 0644); err != nil {
		t.Fatalf("Failed to create passing test: %v", err)
	}

	// Create __test__/failing directory with failing test
	failingPath := filepath.Join(projectPath, "__test__", "failing")
	if err := os.MkdirAll(failingPath, 0755); err != nil {
		t.Fatalf("Failed to create failing test directory: %v", err)
	}

	failingTest := `DO $$ BEGIN RAISE EXCEPTION 'This test should be filtered out'; END $$;`
	if err := os.WriteFile(filepath.Join(failingPath, "test_fail.sql"), []byte(failingTest), 0644); err != nil {
		t.Fatalf("Failed to create failing test: %v", err)
	}

	// Create deploy.sql with filtered pgmi_test() - only run passing tests
	deploySQL := `
-- Execute only tests in the 'passing' directory
SELECT pgmi_test('./passing/**');
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createDirectModeProjectSavepointTest(t *testing.T, projectPath string) {
	t.Helper()

	// Create migrations
	migrationsPath := filepath.Join(projectPath, "migrations")
	if err := os.MkdirAll(migrationsPath, 0755); err != nil {
		t.Fatalf("Failed to create migrations directory: %v", err)
	}

	migration := `CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`
	if err := os.WriteFile(filepath.Join(migrationsPath, "001_users.sql"), []byte(migration), 0644); err != nil {
		t.Fatalf("Failed to create migration: %v", err)
	}

	// Create test that inserts data
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `
-- Insert test data (should be rolled back)
INSERT INTO users (name) VALUES ('test_user_1'), ('test_user_2');

DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM users;
    IF v_count != 2 THEN
        RAISE EXCEPTION 'Expected 2 users, got %', v_count;
    END IF;
    RAISE NOTICE '✓ Test data inserted correctly';
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_insert.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql that runs migrations then tests
	deploySQL := `
-- Execute migrations
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN
        SELECT content
        FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Tests execute in savepoint and get rolled back
SELECT pgmi_test();
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createDirectModeProjectWithFixture(t *testing.T, projectPath string) {
	t.Helper()

	// Create migrations
	migrationsPath := filepath.Join(projectPath, "migrations")
	if err := os.MkdirAll(migrationsPath, 0755); err != nil {
		t.Fatalf("Failed to create migrations directory: %v", err)
	}

	migration := `CREATE TABLE products (id SERIAL PRIMARY KEY, name TEXT, price NUMERIC);`
	if err := os.WriteFile(filepath.Join(migrationsPath, "001_products.sql"), []byte(migration), 0644); err != nil {
		t.Fatalf("Failed to create migration: %v", err)
	}

	// Create test directory with fixture
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	// Fixture file (00_ prefix means it runs first)
	fixture := `
INSERT INTO products (name, price) VALUES
    ('Widget', 9.99),
    ('Gadget', 19.99),
    ('Gizmo', 29.99);
`
	if err := os.WriteFile(filepath.Join(testPath, "00_fixture.sql"), []byte(fixture), 0644); err != nil {
		t.Fatalf("Failed to create fixture: %v", err)
	}

	// Test that depends on fixture data
	test := `
DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM products;
    IF v_count != 3 THEN
        RAISE EXCEPTION 'Expected 3 products from fixture, got %', v_count;
    END IF;
    RAISE NOTICE '✓ Fixture data loaded correctly';
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_products.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql
	deploySQL := `
-- Execute migrations
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN
        SELECT content
        FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Execute tests with fixtures
SELECT pgmi_test();
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createDirectModeProjectNoTests(t *testing.T, projectPath string) {
	t.Helper()

	// Create deploy.sql with pgmi_test() but no __test__ directory
	deploySQL := `
-- Execute tests (none exist, should be no-op)
SELECT pgmi_test();

-- Simple statement to verify deploy.sql completed
SELECT 1;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}
