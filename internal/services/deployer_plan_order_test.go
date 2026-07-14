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

// TestPlanOrder_MixedSortKeysAndPathFallback pins pgmi_plan_view.execution_order to
// C byte order. sort_key mixes two domains — user sortKeys ("001/000") and path
// fallbacks ("./migrations/...") — and under a linguistic collation like en_US.utf8
// '.' sorts after digits, silently inverting the two groups. The deployment order
// must not depend on the server's locale.
func TestPlanOrder_MixedSortKeysAndPathFallback(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createMixedSortKeyProject(t, projectPath)

	testDB := "pgmi_plan_collation"
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

	rows, err := pool.Query(ctx, `SELECT path FROM plan_order_capture ORDER BY execution_order`)
	if err != nil {
		t.Fatalf("Failed to query plan_order_capture: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatalf("Failed to scan: %v", err)
		}
		got = append(got, path)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("Row iteration failed: %v", err)
	}

	// C byte order: '.' (0x2E) < '0' (0x30), so both path fallbacks precede
	// every numeric sort key. A locale-sensitive collation reverses this.
	want := []string{
		"./migrations/002_data.sql",
		"./setup/functions.sql",
		"./first.sql",
		"./last.sql",
	}
	if !sliceEqual(got, want) {
		t.Errorf("execution_order is not C byte order\nGot:      %v\nExpected: %v", got, want)
	}
}

// TestDeployError_ReportsLineAfterMacroExpansion proves the reported line is the
// line of the script pgmi actually sent, not of the file on disk. CALL pgmi_test()
// expands into many lines before the syntax error, so a naive line count against
// deploy.sql would point at the wrong statement.
func TestDeployError_ReportsLineAfterMacroExpansion(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()

	testDir := filepath.Join(projectPath, "__test__")
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatalf("Failed to create __test__: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "test_noop.sql"), []byte(`SELECT 1;`), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// The macro expands to many statements, so "SELEC 1;" sits on line 4 of
	// deploy.sql but much further down in the script PostgreSQL parses.
	deploySQL := "BEGIN;\nCALL pgmi_test();\nCOMMIT;\nSELEC 1;\n"
	const rawLine = 4

	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}

	testDB := "pgmi_error_position"
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
		t.Fatal("expected the syntax error to fail the deploy")
	}

	loc := pgmi.LocateError(err)
	if loc == nil {
		t.Fatalf("expected a resolved error location, got none from: %v", err)
	}

	// The mapping is correct if and only if the reported line holds the bad SQL.
	if loc.SourceLine != "SELEC 1;" {
		t.Errorf("reported line %d does not hold the offending statement: got %q, want %q",
			loc.Line, loc.SourceLine, "SELEC 1;")
	}
	if !loc.Expanded {
		t.Error("expected the location to be flagged as expanded — deploy.sql contained a pgmi_test() macro")
	}
	if loc.Line <= rawLine {
		t.Errorf("expected macro expansion to push the error past line %d of the file on disk, got line %d",
			rawLine, loc.Line)
	}

	if out := pgmi.FormatError(err); !strings.Contains(out, "LINE ") {
		t.Errorf("FormatError should point at the offending line, got:\n%s", out)
	}
}

func createMixedSortKeyProject(t *testing.T, projectPath string) {
	t.Helper()

	for _, dir := range []string{"migrations", "setup"} {
		if err := os.MkdirAll(filepath.Join(projectPath, dir), 0755); err != nil {
			t.Fatalf("Failed to create %s: %v", dir, err)
		}
	}

	files := map[string]string{
		filepath.Join("migrations", "002_data.sql"): "SELECT 'no metadata: sorts by path';\n",
		filepath.Join("setup", "functions.sql"):     "SELECT 'no metadata: sorts by path';\n",
		"first.sql": `/*
<pgmi-meta id="c0000001-0001-4000-8000-000000000001" idempotent="true">
  <sortKeys><key>001/000</key></sortKeys>
</pgmi-meta>
*/
SELECT 'sortKey 001/000';
`,
		"last.sql": `/*
<pgmi-meta id="c0000001-0001-4000-8000-000000000002" idempotent="true">
  <sortKeys><key>999/000</key></sortKeys>
</pgmi-meta>
*/
SELECT 'sortKey 999/000';
`,
	}

	for name, content := range files {
		if err := os.WriteFile(filepath.Join(projectPath, name), []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create %s: %v", name, err)
		}
	}

	deploySQL := `
CREATE TABLE plan_order_capture (
    execution_order BIGINT PRIMARY KEY,
    sort_key TEXT NOT NULL,
    path TEXT NOT NULL
);

INSERT INTO plan_order_capture (execution_order, sort_key, path)
SELECT execution_order, sort_key, path FROM pg_temp.pgmi_plan_view;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
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
