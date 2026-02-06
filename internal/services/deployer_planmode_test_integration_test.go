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

// TestDeploymentService_PlanMode_PassingTests verifies that pgmi_plan_test() macro
// schedules tests via pgmi_plan_command() and they execute during plan execution phase.
func TestDeploymentService_PlanMode_PassingTests(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createPlanModeProjectPassing(t, projectPath)

	testDB := "pgmi_plan_mode_pass"
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
		t.Fatalf("Deploy with passing tests (plan mode) failed: %v", err)
	}

	// Verify deployment artifacts exist
	pool := testhelpers.GetTestPool(t, connString, testDB)

	var tableExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_name = 'products'
		)
	`).Scan(&tableExists)
	if err != nil {
		t.Fatalf("Failed to check table existence: %v", err)
	}
	if !tableExists {
		t.Error("Expected 'products' table to exist after deployment")
	}
}

// TestDeploymentService_PlanMode_FailingTests verifies that pgmi_plan_test() macro
// causes deployment to fail when a scheduled test raises an exception.
func TestDeploymentService_PlanMode_FailingTests(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createPlanModeProjectFailing(t, projectPath)

	testDB := "pgmi_plan_mode_fail"
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
		t.Fatal("Expected deployment to fail due to failing test in plan mode")
	}

	// Error should mention test failure
	if !strings.Contains(err.Error(), "Plan mode test failure") {
		t.Errorf("Expected error to contain 'Plan mode test failure', got: %v", err)
	}
}

// TestDeploymentService_PlanMode_FilterPattern verifies that pgmi_plan_test('./path/**')
// only schedules tests matching the specified pattern.
func TestDeploymentService_PlanMode_FilterPattern(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createPlanModeProjectWithFilter(t, projectPath)

	testDB := "pgmi_plan_mode_filter"
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
		t.Fatalf("Deploy with filtered tests (plan mode) failed: %v", err)
	}
}

// TestDeploymentService_PlanMode_SavepointStructure verifies that pgmi_plan_test()
// properly schedules SAVEPOINT and ROLLBACK commands around tests.
func TestDeploymentService_PlanMode_SavepointStructure(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createPlanModeProjectSavepointTest(t, projectPath)

	testDB := "pgmi_plan_mode_savepoint"
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
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM orders").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query orders table: %v", err)
	}

	if count != 0 {
		t.Errorf("Expected 0 orders (test data should be rolled back), got %d", count)
	}
}

// TestDeploymentService_PlanMode_MixedWithDirectCommands verifies that pgmi_plan_test()
// works correctly when mixed with other pgmi_plan_command() calls.
func TestDeploymentService_PlanMode_MixedWithDirectCommands(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createPlanModeProjectMixed(t, projectPath)

	testDB := "pgmi_plan_mode_mixed"
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
		t.Fatalf("Deploy with mixed commands failed: %v", err)
	}

	// Verify both migrations and post-deploy commands ran
	pool := testhelpers.GetTestPool(t, connString, testDB)

	var settingsCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM app_settings").Scan(&settingsCount)
	if err != nil {
		t.Fatalf("Failed to query settings: %v", err)
	}

	if settingsCount == 0 {
		t.Error("Expected app_settings to have data from post-deploy commands")
	}
}

// TestDeploymentService_PlanMode_NoTests verifies that pgmi_plan_test() with no
// matching tests produces no commands and doesn't cause errors.
func TestDeploymentService_PlanMode_NoTests(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createPlanModeProjectNoTests(t, projectPath)

	testDB := "pgmi_plan_mode_no_tests"
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
		t.Fatalf("Deploy with no tests (plan mode) failed: %v", err)
	}
}

// Helper functions

func createPlanModeProjectPassing(t *testing.T, projectPath string) {
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

	// Create test directory
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'products') THEN
        RAISE EXCEPTION 'products table not found';
    END IF;
    RAISE NOTICE '✓ products table exists (plan mode test)';
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_products.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql using pgmi_plan_test() macro inside DO block
	deploySQL := `
-- Schedule migrations
DO $$
DECLARE
    v_file RECORD;
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    FOR v_file IN
        SELECT path, content
        FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        RAISE NOTICE 'Scheduling migration: %', v_file.path;
        PERFORM pg_temp.pgmi_plan_command(v_file.content);
    END LOOP;

    -- Schedule savepoint before tests
    PERFORM pg_temp.pgmi_plan_command('SAVEPOINT before_tests;');

    -- Schedule tests using pgmi_plan_test() macro (expands to PERFORM calls)
    pgmi_plan_test();

    -- Schedule rollback and commit
    PERFORM pg_temp.pgmi_plan_command('ROLLBACK TO SAVEPOINT before_tests;');
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createPlanModeProjectFailing(t *testing.T, projectPath string) {
	t.Helper()

	// Create test directory with failing test
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `
DO $$
BEGIN
    RAISE EXCEPTION 'Plan mode test failure';
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_fail.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql
	deploySQL := `
DO $$
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- Schedule tests (will fail)
    pgmi_plan_test();

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createPlanModeProjectWithFilter(t *testing.T, projectPath string) {
	t.Helper()

	// Create __test__/passing with passing test
	passingPath := filepath.Join(projectPath, "__test__", "passing")
	if err := os.MkdirAll(passingPath, 0755); err != nil {
		t.Fatalf("Failed to create passing test directory: %v", err)
	}

	passingTest := `DO $$ BEGIN RAISE NOTICE '✓ Passing test (plan mode)'; END $$;`
	if err := os.WriteFile(filepath.Join(passingPath, "test_pass.sql"), []byte(passingTest), 0644); err != nil {
		t.Fatalf("Failed to create passing test: %v", err)
	}

	// Create __test__/failing with failing test
	failingPath := filepath.Join(projectPath, "__test__", "failing")
	if err := os.MkdirAll(failingPath, 0755); err != nil {
		t.Fatalf("Failed to create failing test directory: %v", err)
	}

	failingTest := `DO $$ BEGIN RAISE EXCEPTION 'This test should be filtered out (plan mode)'; END $$;`
	if err := os.WriteFile(filepath.Join(failingPath, "test_fail.sql"), []byte(failingTest), 0644); err != nil {
		t.Fatalf("Failed to create failing test: %v", err)
	}

	// Create deploy.sql - only schedule passing tests
	deploySQL := `
DO $$
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- Only schedule tests in passing directory
    pgmi_plan_test('./passing/**');

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createPlanModeProjectSavepointTest(t *testing.T, projectPath string) {
	t.Helper()

	// Create migrations
	migrationsPath := filepath.Join(projectPath, "migrations")
	if err := os.MkdirAll(migrationsPath, 0755); err != nil {
		t.Fatalf("Failed to create migrations directory: %v", err)
	}

	migration := `CREATE TABLE orders (id SERIAL PRIMARY KEY, amount NUMERIC);`
	if err := os.WriteFile(filepath.Join(migrationsPath, "001_orders.sql"), []byte(migration), 0644); err != nil {
		t.Fatalf("Failed to create migration: %v", err)
	}

	// Create test that inserts data
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `
INSERT INTO orders (amount) VALUES (100.00), (200.00), (300.00);

DO $$
DECLARE
    v_count INT;
BEGIN
    SELECT COUNT(*) INTO v_count FROM orders;
    IF v_count != 3 THEN
        RAISE EXCEPTION 'Expected 3 orders, got %', v_count;
    END IF;
    RAISE NOTICE '✓ Test orders inserted (plan mode)';
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_orders.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql with proper savepoint structure
	deploySQL := `
DO $$
DECLARE
    v_file RECORD;
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- Run migrations
    FOR v_file IN
        SELECT content
        FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        PERFORM pg_temp.pgmi_plan_command(v_file.content);
    END LOOP;

    -- Tests in savepoint
    PERFORM pg_temp.pgmi_plan_command('SAVEPOINT before_tests;');
    pgmi_plan_test();
    PERFORM pg_temp.pgmi_plan_command('ROLLBACK TO SAVEPOINT before_tests;');

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createPlanModeProjectMixed(t *testing.T, projectPath string) {
	t.Helper()

	// Create migrations
	migrationsPath := filepath.Join(projectPath, "migrations")
	if err := os.MkdirAll(migrationsPath, 0755); err != nil {
		t.Fatalf("Failed to create migrations directory: %v", err)
	}

	migration := `CREATE TABLE app_settings (key TEXT PRIMARY KEY, value TEXT);`
	if err := os.WriteFile(filepath.Join(migrationsPath, "001_settings.sql"), []byte(migration), 0644); err != nil {
		t.Fatalf("Failed to create migration: %v", err)
	}

	// Create test
	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'app_settings') THEN
        RAISE EXCEPTION 'app_settings table not found';
    END IF;
END $$;
`
	if err := os.WriteFile(filepath.Join(testPath, "test_settings.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Create deploy.sql mixing migrations, tests, and post-deploy commands
	deploySQL := `
DO $$
DECLARE
    v_file RECORD;
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- Run migrations
    FOR v_file IN
        SELECT content FROM pg_temp.pgmi_source
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        PERFORM pg_temp.pgmi_plan_command(v_file.content);
    END LOOP;

    -- Tests in savepoint
    PERFORM pg_temp.pgmi_plan_command('SAVEPOINT before_tests;');
    pgmi_plan_test();
    PERFORM pg_temp.pgmi_plan_command('ROLLBACK TO SAVEPOINT before_tests;');

    -- Post-deploy: insert settings
    PERFORM pg_temp.pgmi_plan_command($cmd$
        INSERT INTO app_settings (key, value) VALUES
            ('app_version', '1.0.0'),
            ('deployed_at', NOW()::TEXT)
        ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
    $cmd$);

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createPlanModeProjectNoTests(t *testing.T, projectPath string) {
	t.Helper()

	// Create deploy.sql with pgmi_plan_test() but no __test__ directory
	deploySQL := `
DO $$
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    -- No tests exist, should produce no commands
    pgmi_plan_test();

    PERFORM pg_temp.pgmi_plan_command('SELECT 1;');
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}
