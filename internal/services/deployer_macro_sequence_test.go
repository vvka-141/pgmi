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

// TestMacroSequence_NoFixture verifies the event sequence for a simple test
// without fixtures by capturing persisted events via INSERT callback.
//
// Note: Events inside savepoints (test_start, test_end, rollback, teardown_start)
// are rolled back. Only suite_start, teardown_end, and suite_end persist.
// For full sequence verification, see the unit tests that exercise the
// generator directly.
func TestMacroSequence_NoFixture(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createSequenceProjectNoFixture(t, projectPath)

	testDB := "pgmi_seq_no_fixture"
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

	// Verify persisted events
	pool := testhelpers.GetTestPool(t, connString, testDB)

	// Query persisted events (suite_start, teardown_end, suite_end)
	rows, err := pool.Query(ctx, `SELECT event, ordinal FROM test_event_log ORDER BY id`)
	if err != nil {
		t.Fatalf("Failed to query event log: %v", err)
	}
	defer rows.Close()

	var events []string
	for rows.Next() {
		var event string
		var ordinal int
		if err := rows.Scan(&event, &ordinal); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}
		events = append(events, event)
	}

	// Only these events persist (outside savepoints)
	expected := []string{"suite_start", "teardown_end", "suite_end"}
	if !sliceEqual(events, expected) {
		t.Errorf("Persisted event sequence mismatch\nGot:      %v\nExpected: %v", events, expected)
	}
}

// TestMacroSequence_WithFixture verifies the event sequence when a fixture is present.
// Same caveat: only events outside savepoints persist.
func TestMacroSequence_WithFixture(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createSequenceProjectWithFixture(t, projectPath)

	testDB := "pgmi_seq_with_fixture"
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

	// Verify persisted events
	pool := testhelpers.GetTestPool(t, connString, testDB)

	rows, err := pool.Query(ctx, `SELECT event FROM test_event_log ORDER BY id`)
	if err != nil {
		t.Fatalf("Failed to query event log: %v", err)
	}
	defer rows.Close()

	var events []string
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}
		events = append(events, event)
	}

	// Only these events persist
	expected := []string{"suite_start", "teardown_end", "suite_end"}
	if !sliceEqual(events, expected) {
		t.Errorf("Persisted event sequence mismatch\nGot:      %v\nExpected: %v", events, expected)
	}
}

// TestMacroSequence_NestedDirectories verifies that nested test directories work correctly.
// Due to PostgreSQL savepoint nesting, only the ROOT's teardown_end persists to the table.
// Child/grandchild teardown_end callbacks happen inside the root's dir savepoint and get
// rolled back when root does ROLLBACK TO SAVEPOINT.
// For full DFS sequence verification, see TestMacroSequence_NestedDirectories_Notices.
func TestMacroSequence_NestedDirectories(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createSequenceProjectNested(t, projectPath)

	testDB := "pgmi_seq_nested"
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

	pool := testhelpers.GetTestPool(t, connString, testDB)

	// Query all persisted events
	rows, err := pool.Query(ctx, `SELECT event, depth, directory FROM test_event_log ORDER BY id`)
	if err != nil {
		t.Fatalf("Failed to query events: %v", err)
	}
	defer rows.Close()

	var events []string
	var teardownDepth int
	for rows.Next() {
		var event string
		var depth int
		var dir *string
		if err := rows.Scan(&event, &depth, &dir); err != nil {
			t.Fatalf("Failed to scan row: %v", err)
		}
		events = append(events, event)
		if event == "teardown_end" {
			teardownDepth = depth
		}
	}

	// Due to savepoint nesting, only root teardown_end persists
	// (child/grandchild teardown_end are inside root's dir savepoint)
	expected := []string{"suite_start", "teardown_end", "suite_end"}
	if !sliceEqual(events, expected) {
		t.Errorf("Persisted events mismatch\nGot:      %v\nExpected: %v", events, expected)
	}

	// The persisted teardown_end should be the root (depth=0)
	if teardownDepth != 0 {
		t.Errorf("Persisted teardown_end should be root (depth=0), got depth=%d", teardownDepth)
	}
}

// TestMacroSequence_TestFailure verifies that test failures include script path in error.
func TestMacroSequence_TestFailure(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createSequenceProjectTestFailure(t, projectPath)

	testDB := "pgmi_seq_test_fail"
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
		t.Fatal("Expected deployment to fail due to test failure")
	}

	if !strings.Contains(err.Error(), "Intentional failure") {
		t.Errorf("Expected error to contain 'Intentional failure', got: %v", err)
	}
}

// TestMacroSequence_FixtureFailure verifies that fixture failures prevent test execution.
func TestMacroSequence_FixtureFailure(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createSequenceProjectFixtureFailure(t, projectPath)

	testDB := "pgmi_seq_fixture_fail"
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
		t.Fatal("Expected deployment to fail due to fixture failure")
	}

	if !strings.Contains(err.Error(), "Fixture explosion") {
		t.Errorf("Expected error to contain 'Fixture explosion', got: %v", err)
	}
}

// Helper functions

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// createSequenceProjectNoFixture creates a project with event logging callback
func createSequenceProjectNoFixture(t *testing.T, projectPath string) {
	t.Helper()

	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `DO $$ BEGIN RAISE NOTICE 'Simple test passed'; END $$;`
	if err := os.WriteFile(filepath.Join(testPath, "test_simple.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	deploySQL := `
-- Create event log table
CREATE TABLE test_event_log (
    id SERIAL PRIMARY KEY,
    event TEXT NOT NULL,
    path TEXT,
    directory TEXT,
    depth INT,
    ordinal INT
);

-- Create event logging callback
CREATE OR REPLACE FUNCTION pg_temp.event_logger(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $cb$
BEGIN
    INSERT INTO test_event_log (event, path, directory, depth, ordinal)
    VALUES (e.event, e.path, e.directory, e.depth, e.ordinal);
END $cb$;

-- Execute tests with logging callback
BEGIN;
CALL pgmi_test(NULL, 'pg_temp.event_logger');
COMMIT;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createSequenceProjectWithFixture(t *testing.T, projectPath string) {
	t.Helper()

	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	fixture := `CREATE TEMP TABLE fixture_data (id int);`
	if err := os.WriteFile(filepath.Join(testPath, "_setup.sql"), []byte(fixture), 0644); err != nil {
		t.Fatalf("Failed to create fixture: %v", err)
	}

	test := `DO $$ BEGIN RAISE NOTICE 'Test with fixture'; END $$;`
	if err := os.WriteFile(filepath.Join(testPath, "test_fixture.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	deploySQL := `
CREATE TABLE test_event_log (
    id SERIAL PRIMARY KEY,
    event TEXT NOT NULL,
    path TEXT,
    directory TEXT,
    depth INT,
    ordinal INT
);

CREATE OR REPLACE FUNCTION pg_temp.event_logger(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $cb$
BEGIN
    INSERT INTO test_event_log (event, path, directory, depth, ordinal)
    VALUES (e.event, e.path, e.directory, e.depth, e.ordinal);
END $cb$;

BEGIN;
CALL pgmi_test(NULL, 'pg_temp.event_logger');
COMMIT;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createSequenceProjectNested(t *testing.T, projectPath string) {
	t.Helper()

	// Root test directory
	rootPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		t.Fatalf("Failed to create root __test__: %v", err)
	}

	rootFixture := `CREATE TEMP TABLE root_marker (id int);`
	if err := os.WriteFile(filepath.Join(rootPath, "_setup.sql"), []byte(rootFixture), 0644); err != nil {
		t.Fatalf("Failed to create root fixture: %v", err)
	}

	rootTest := `DO $$ BEGIN RAISE NOTICE 'Root test'; END $$;`
	if err := os.WriteFile(filepath.Join(rootPath, "test_root.sql"), []byte(rootTest), 0644); err != nil {
		t.Fatalf("Failed to create root test: %v", err)
	}

	// Child directory
	childPath := filepath.Join(rootPath, "child")
	if err := os.MkdirAll(childPath, 0755); err != nil {
		t.Fatalf("Failed to create child directory: %v", err)
	}

	childFixture := `CREATE TEMP TABLE child_marker (id int);`
	if err := os.WriteFile(filepath.Join(childPath, "_setup.sql"), []byte(childFixture), 0644); err != nil {
		t.Fatalf("Failed to create child fixture: %v", err)
	}

	childTest := `DO $$ BEGIN RAISE NOTICE 'Child test'; END $$;`
	if err := os.WriteFile(filepath.Join(childPath, "test_child.sql"), []byte(childTest), 0644); err != nil {
		t.Fatalf("Failed to create child test: %v", err)
	}

	// Grandchild directory
	grandchildPath := filepath.Join(childPath, "grandchild")
	if err := os.MkdirAll(grandchildPath, 0755); err != nil {
		t.Fatalf("Failed to create grandchild directory: %v", err)
	}

	grandchildFixture := `CREATE TEMP TABLE grandchild_marker (id int);`
	if err := os.WriteFile(filepath.Join(grandchildPath, "_setup.sql"), []byte(grandchildFixture), 0644); err != nil {
		t.Fatalf("Failed to create grandchild fixture: %v", err)
	}

	grandchildTest := `DO $$ BEGIN RAISE NOTICE 'Grandchild test'; END $$;`
	if err := os.WriteFile(filepath.Join(grandchildPath, "test_deep.sql"), []byte(grandchildTest), 0644); err != nil {
		t.Fatalf("Failed to create grandchild test: %v", err)
	}

	deploySQL := `
CREATE TABLE test_event_log (
    id SERIAL PRIMARY KEY,
    event TEXT NOT NULL,
    path TEXT,
    directory TEXT,
    depth INT,
    ordinal INT
);

CREATE OR REPLACE FUNCTION pg_temp.event_logger(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $cb$
BEGIN
    INSERT INTO test_event_log (event, path, directory, depth, ordinal)
    VALUES (e.event, e.path, e.directory, e.depth, e.ordinal);
END $cb$;

BEGIN;
CALL pgmi_test(NULL, 'pg_temp.event_logger');
COMMIT;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createSequenceProjectTestFailure(t *testing.T, projectPath string) {
	t.Helper()

	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	test := `DO $$ BEGIN RAISE EXCEPTION 'Intentional failure'; END $$;`
	if err := os.WriteFile(filepath.Join(testPath, "test_fail.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	deploySQL := `BEGIN; CALL pgmi_test(); COMMIT;`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createSequenceProjectFixtureFailure(t *testing.T, projectPath string) {
	t.Helper()

	testPath := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testPath, 0755); err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	fixture := `DO $$ BEGIN RAISE EXCEPTION 'Fixture explosion'; END $$;`
	if err := os.WriteFile(filepath.Join(testPath, "_setup.sql"), []byte(fixture), 0644); err != nil {
		t.Fatalf("Failed to create fixture: %v", err)
	}

	test := `DO $$ BEGIN RAISE NOTICE 'This should never run'; END $$;`
	if err := os.WriteFile(filepath.Join(testPath, "test_should_not_run.sql"), []byte(test), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	deploySQL := `BEGIN; CALL pgmi_test(); COMMIT;`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}
