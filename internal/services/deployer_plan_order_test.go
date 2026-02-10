package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestPlanOrder_NestedDirectories verifies pgmi_test_plan() returns correct pre-order DFS:
// fixture(0) → test(0) → fixture(1) → test(1) → ... → teardown(N) → ... → teardown(0)
// Due to PostgreSQL savepoint nesting, only the root teardown_end persists to the table.
// Full plan order is verified in TestPlanDebug_QueryPlanDirectly which queries pgmi_test_plan() directly.
func TestPlanOrder_NestedDirectories(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createDiagnosticNestedProject(t, projectPath)

	testDB := "pgmi_plan_order"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy to set up the tables
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

	// Query the plan order
	pool := testhelpers.GetTestPool(t, connString, testDB)

	// Check the actual event order from our custom callback
	rows, err := pool.Query(ctx, `
		SELECT id, event, path, directory, depth, ordinal
		FROM plan_order_log
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("Failed to query plan_order_log: %v", err)
	}
	defer rows.Close()

	t.Log("Plan execution order:")
	var events []string
	var teardownDepth int
	for rows.Next() {
		var id, depth, ordinal int
		var event string
		var path, directory *string
		if err := rows.Scan(&id, &event, &path, &directory, &depth, &ordinal); err != nil {
			t.Fatalf("Failed to scan: %v", err)
		}
		pathStr := "<nil>"
		if path != nil {
			pathStr = *path
		}
		dirStr := "<nil>"
		if directory != nil {
			dirStr = *directory
		}
		t.Logf("  %d: %s path=%s dir=%s depth=%d ordinal=%d", id, event, pathStr, dirStr, depth, ordinal)

		events = append(events, event)
		if event == "teardown_end" {
			teardownDepth = depth
		}
	}

	// Due to savepoint nesting, only root teardown_end persists
	expected := []string{"suite_start", "teardown_end", "suite_end"}
	if !sliceEqual(events, expected) {
		t.Errorf("Persisted events mismatch\nGot:      %v\nExpected: %v", events, expected)
	}

	// The persisted teardown_end should be the root (depth=0)
	if teardownDepth != 0 {
		t.Errorf("Persisted teardown_end should be root (depth=0), got depth=%d", teardownDepth)
	}

	t.Log("Note: Full DFS ordering verified in TestPlanDebug_QueryPlanDirectly")
}

func createDiagnosticNestedProject(t *testing.T, projectPath string) {
	t.Helper()

	// Create nested test directories
	rootPath := filepath.Join(projectPath, "__test__")
	childPath := filepath.Join(rootPath, "child")
	grandchildPath := filepath.Join(childPath, "grandchild")

	for _, dir := range []string{rootPath, childPath, grandchildPath} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create %s: %v", dir, err)
		}
	}

	// Root level
	if err := os.WriteFile(filepath.Join(rootPath, "_setup.sql"), []byte(`SELECT 1;`), 0644); err != nil {
		t.Fatalf("Failed to create root fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "test_root.sql"), []byte(`SELECT 'root';`), 0644); err != nil {
		t.Fatalf("Failed to create root test: %v", err)
	}

	// Child level
	if err := os.WriteFile(filepath.Join(childPath, "_setup.sql"), []byte(`SELECT 2;`), 0644); err != nil {
		t.Fatalf("Failed to create child fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(childPath, "test_child.sql"), []byte(`SELECT 'child';`), 0644); err != nil {
		t.Fatalf("Failed to create child test: %v", err)
	}

	// Grandchild level
	if err := os.WriteFile(filepath.Join(grandchildPath, "_setup.sql"), []byte(`SELECT 3;`), 0644); err != nil {
		t.Fatalf("Failed to create grandchild fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(grandchildPath, "test_deep.sql"), []byte(`SELECT 'grandchild';`), 0644); err != nil {
		t.Fatalf("Failed to create grandchild test: %v", err)
	}

	// Deploy.sql that logs ALL events
	deploySQL := `
-- Create logging table
CREATE TABLE plan_order_log (
    id SERIAL PRIMARY KEY,
    event TEXT NOT NULL,
    path TEXT,
    directory TEXT,
    depth INT,
    ordinal INT
);

-- Create callback that logs ALL events
CREATE OR REPLACE FUNCTION pg_temp.plan_logger(e pg_temp.pgmi_test_event)
RETURNS void LANGUAGE plpgsql AS $cb$
BEGIN
    INSERT INTO plan_order_log (event, path, directory, depth, ordinal)
    VALUES (e.event, e.path, e.directory, e.depth, e.ordinal);
END $cb$;

-- Execute tests with logging
BEGIN;
CALL pgmi_test(NULL, 'pg_temp.plan_logger');
COMMIT;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}
