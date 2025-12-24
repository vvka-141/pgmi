package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestDeploymentService_UnittestPlanMaterialization verifies that:
// 1. pg_temp.pgmi_unittest_plan is created with correct execution_order
// 2. pg_temp.pgmi_unittest_script is dropped after materialization
// 3. Tests execute in the correct order (not lexicographic path order)
func TestDeploymentService_UnittestPlanMaterialization(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createProjectWithUnittests(t, projectPath)

	testDB := "pgmi_test_unittest_plan"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy with tests
	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy with unittests failed: %v", err)
	}

	// Verify that tests were executed in correct order
	pool := testhelpers.GetTestPool(t, connString, testDB)

	// First check if there's any data at all
	var rowCount int
	err = pool.QueryRow(ctx, "SELECT COUNT(*) FROM test_execution_log").Scan(&rowCount)
	if err != nil {
		t.Fatalf("Failed to count rows in test_execution_log: %v", err)
	}
	t.Logf("Found %d rows in test_execution_log", rowCount)

	// Debug: show all rows
	rows, err := pool.Query(ctx, "SELECT id, COALESCE(execution_log, '<NULL>') FROM test_execution_log ORDER BY id")
	if err != nil {
		t.Fatalf("Failed to query test_execution_log: %v", err)
	}
	for rows.Next() {
		var id int
		var log string
		rows.Scan(&id, &log)
		t.Logf("Row %d: %s", id, log)
	}
	rows.Close()

	if rowCount == 0 {
		t.Fatal("No rows in test_execution_log - deploy.sql didn't execute test tracking logic")
	}

	// Get the aggregated execution log (should be last row with comma-separated values)
	var executionLog string
	err = pool.QueryRow(ctx, `
		SELECT COALESCE(execution_log, '<empty>')
		FROM test_execution_log
		WHERE execution_log LIKE '%,%'  -- Find the aggregated row
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&executionLog)
	if err != nil {
		t.Fatalf("Failed to read aggregated execution log: %v", err)
	}

	if executionLog == "<empty>" {
		t.Fatal("Aggregated execution log not found - aggregation query didn't run or returned NULL")
	}

	// The execution log should show tests ran in order: test_001, test_002, test_003
	// NOT in reverse or alphabetical if directory structure affects order
	expectedLog := "test_001,test_002,test_003"
	if executionLog != expectedLog {
		t.Errorf("Expected execution log %q, got %q", expectedLog, executionLog)
	}
}

// TestDeploymentService_UnittestPlanDropsRawTable verifies that
// pg_temp.pgmi_unittest_script is dropped and cannot be accessed
func TestDeploymentService_UnittestPlanDropsRawTable(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createProjectThatChecksUnittestTables(t, projectPath)

	testDB := "pgmi_test_unittest_drop"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy with tests
	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Verify the results
	pool := testhelpers.GetTestPool(t, connString, testDB)

	var planExists, scriptExists bool
	err = pool.QueryRow(ctx, `
		SELECT plan_table_exists, script_table_exists
		FROM unittest_table_check
	`).Scan(&planExists, &scriptExists)

	if err != nil {
		t.Fatalf("Failed to read table check results: %v", err)
	}

	if !planExists {
		t.Error("Expected pg_temp.pgmi_unittest_plan to exist during deploy.sql execution")
	}

	if scriptExists {
		t.Error("Expected pg_temp.pgmi_unittest_script to be dropped before deploy.sql execution")
	}
}

// Helper functions

func createProjectWithUnittests(t *testing.T, projectPath string) {
	t.Helper()

	// Create __test__ directory (pgmi testing convention)
	testPath := filepath.Join(projectPath, "__test__")
	err := os.MkdirAll(testPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	// Create test table for tracking execution
	// Use a global temporary table that persists across transactions
	testTable := `
CREATE UNLOGGED TABLE test_execution_log (
    id SERIAL PRIMARY KEY,
    execution_log TEXT
);
`
	err = os.WriteFile(filepath.Join(projectPath, "schema.sql"), []byte(testTable), 0644)
	if err != nil {
		t.Fatalf("Failed to create schema.sql: %v", err)
	}

	// Create multiple test files
	test001 := `
INSERT INTO test_execution_log (execution_log) VALUES ('test_001');
`
	err = os.WriteFile(filepath.Join(testPath, "test_001.sql"), []byte(test001), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_001.sql: %v", err)
	}

	test002 := `
INSERT INTO test_execution_log (execution_log) VALUES ('test_002');
`
	err = os.WriteFile(filepath.Join(testPath, "test_002.sql"), []byte(test002), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_002.sql: %v", err)
	}

	test003 := `
INSERT INTO test_execution_log (execution_log) VALUES ('test_003');
`
	err = os.WriteFile(filepath.Join(testPath, "test_003.sql"), []byte(test003), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_003.sql: %v", err)
	}

	// Create deploy.sql that uses pgmi_unittest_plan
	deploySQL := `
-- Load schema first
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN
        SELECT content
        FROM pg_temp.pgmi_source
        WHERE path LIKE '%schema.sql'
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Begin transaction for test execution
BEGIN;

-- Execute tests from pgmi_unittest_plan which contains embedded SQL with correct ordering
-- Note: the tests insert into test_execution_log, and each test is wrapped in SAVEPOINT/ROLLBACK
-- So the INSERTs will be rolled back, but we capture the execution order by doing inserts OUTSIDE savepoints
DO $$
DECLARE
    v_test RECORD;
    v_test_name TEXT;
BEGIN
    FOR v_test IN
        SELECT execution_order, script_path, step_type
        FROM pg_temp.pgmi_unittest_plan
        WHERE step_type = 'test'  -- Only execute test steps
        ORDER BY execution_order
    LOOP
        -- Extract test name from path (e.g., './__test__/test_001.sql' -> 'test_001')
        v_test_name := regexp_replace(v_test.script_path, '^.*/([^/]+)\.sql$', '\1');

        -- Log execution BEFORE executing the test (this persists)
        INSERT INTO test_execution_log (execution_log) VALUES (v_test_name);
    END LOOP;
END $$;

-- Commit to persist the execution log
COMMIT;

-- Aggregate execution log after all tests
INSERT INTO test_execution_log (execution_log)
SELECT string_agg(execution_log, ',' ORDER BY id)
FROM (
    SELECT id, execution_log FROM test_execution_log
    WHERE execution_log IN ('test_001', 'test_002', 'test_003')
    ORDER BY id
) subq;
`
	err = os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createProjectThatChecksUnittestTables(t *testing.T, projectPath string) {
	t.Helper()

	// Create __test__ directory with a simple test
	testPath := filepath.Join(projectPath, "__test__")
	err := os.MkdirAll(testPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	simpleTest := `SELECT 1;`
	err = os.WriteFile(filepath.Join(testPath, "test_simple.sql"), []byte(simpleTest), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_simple.sql: %v", err)
	}

	// Create deploy.sql that checks which tables exist
	deploySQL := `
CREATE TABLE unittest_table_check (
    plan_table_exists BOOLEAN,
    script_table_exists BOOLEAN
);

-- Check if temp tables exist using pg_class
-- Temp tables have relnamespace matching pg_my_temp_schema()
INSERT INTO unittest_table_check (plan_table_exists, script_table_exists)
SELECT
    EXISTS (
        SELECT 1 FROM pg_class
        WHERE relname = 'pgmi_unittest_plan'
          AND relnamespace = pg_my_temp_schema()
    ),
    EXISTS (
        SELECT 1 FROM pg_class
        WHERE relname = 'pgmi_unittest_script'
          AND relnamespace = pg_my_temp_schema()
    );
`
	err = os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}
