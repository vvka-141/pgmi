package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/params"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
)

// TestPlanDebug_QueryPlanDirectly sets up test data and queries pgmi_test_plan() directly
// to see the raw ordering from the SQL function.
func TestPlanDebug_QueryPlanDirectly(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()

	// Create test project
	projectPath := t.TempDir()
	createDebugNestedProject(t, projectPath)

	// Create test database
	testDB := "pgmi_plan_debug"
	testhelpers.CreateTestDB(t, connString, testDB)
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Connect to test database
	pool := testhelpers.GetTestPool(t, connString, testDB)

	// Need a connection from pool (before schema creation)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	// Initialize session schema
	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	// Scan and load files
	fileScanner := scanner.NewScanner(checksum.New())
	result, err := fileScanner.ScanDirectory(projectPath)
	if err != nil {
		t.Fatalf("Failed to scan directory: %v", err)
	}

	fileLoader := loader.NewLoader()
	if err := fileLoader.LoadFilesIntoSession(ctx, conn, result.Files); err != nil {
		t.Fatalf("Failed to load files: %v", err)
	}

	// Query test directories
	t.Log("Test directories:")
	rows, err := conn.Query(ctx, `SELECT path, parent_path, depth FROM pg_temp._pgmi_test_directory ORDER BY depth, path`)
	if err != nil {
		t.Fatalf("Failed to query directories: %v", err)
	}
	for rows.Next() {
		var path string
		var parent *string
		var depth int
		rows.Scan(&path, &parent, &depth)
		parentStr := "<nil>"
		if parent != nil {
			parentStr = *parent
		}
		t.Logf("  %s parent=%s depth=%d", path, parentStr, depth)
	}
	rows.Close()

	// Query test sources
	t.Log("Test sources:")
	rows, err = conn.Query(ctx, `SELECT path, directory, filename, is_fixture FROM pg_temp._pgmi_test_source ORDER BY path`)
	if err != nil {
		t.Fatalf("Failed to query sources: %v", err)
	}
	for rows.Next() {
		var path, dir, filename string
		var isFixture bool
		rows.Scan(&path, &dir, &filename, &isFixture)
		t.Logf("  %s (dir=%s, fixture=%v)", path, dir, isFixture)
	}
	rows.Close()

	// Debug: Verify PostgreSQL string comparison and collation
	t.Log("Debug: string comparison test:")
	var tildeLtDot bool
	err = conn.QueryRow(ctx, `SELECT '~' < './__test__/child/'`).Scan(&tildeLtDot)
	if err != nil {
		t.Fatalf("Failed string comparison: %v", err)
	}
	t.Logf("  '~' < './__test__/child/': %v (collation-aware)", tildeLtDot)

	var tildeLtDotC bool
	err = conn.QueryRow(ctx, `SELECT '~' < './__test__/child/' COLLATE "C"`).Scan(&tildeLtDotC)
	if err != nil {
		t.Fatalf("Failed C collation comparison: %v", err)
	}
	t.Logf("  '~' < './__test__/child/' COLLATE \"C\": %v (should be FALSE for C collation)", tildeLtDotC)

	var asciiTilde, asciiDot int
	err = conn.QueryRow(ctx, `SELECT ASCII('~'), ASCII('.')`).Scan(&asciiTilde, &asciiDot)
	t.Logf("  ASCII('~')=%d, ASCII('.')=%d", asciiTilde, asciiDot)

	var dbCollation string
	err = conn.QueryRow(ctx, `SELECT datcollate FROM pg_database WHERE datname = current_database()`).Scan(&dbCollation)
	t.Logf("  Database collation: %s", dbCollation)

	t.Log("Debug: array comparison test:")
	var rootExitLtChildEntry bool
	err = conn.QueryRow(ctx, `SELECT ARRAY['./__test__/', '~'] < ARRAY['./__test__/', './__test__/child/', '']`).Scan(&rootExitLtChildEntry)
	if err != nil {
		t.Fatalf("Failed array comparison: %v", err)
	}
	t.Logf("  root_exit < child_entry: %v (expected false by ASCII, actual true!)", rootExitLtChildEntry)

	var childEntryLtRootExit bool
	err = conn.QueryRow(ctx, `SELECT ARRAY['./__test__/', './__test__/child/', ''] < ARRAY['./__test__/', '~']`).Scan(&childEntryLtRootExit)
	if err != nil {
		t.Fatalf("Failed array comparison: %v", err)
	}
	t.Logf("  child_entry < root_exit: %v (expected true by ASCII, actual false!)", childEntryLtRootExit)

	// Debug: Query the visits CTE with sort keys
	t.Log("Debug: visits with sort keys:")
	rows, err = conn.Query(ctx, `
		WITH RECURSIVE
		relevant AS (
			SELECT d.path, d.parent_path, d.depth
			FROM pg_temp._pgmi_test_directory d
			WHERE pg_temp.pgmi_has_tests(d.path, NULL)
		),
		tree AS (
			SELECT path, depth, ARRAY[path] AS ancestry
			FROM relevant
			WHERE parent_path IS NULL OR parent_path NOT IN (SELECT path FROM relevant)
			UNION ALL
			SELECT r.path, r.depth, t.ancestry || r.path
			FROM relevant r JOIN tree t ON r.parent_path = t.path
		),
		visits AS (
			SELECT path, depth, array_append(ancestry, ''::text) AS sort_key, false AS is_exit FROM tree
			UNION ALL
			SELECT path, depth, array_append(ancestry, '~'::text), true FROM tree
		)
		SELECT path, depth, sort_key, is_exit
		FROM visits
		ORDER BY sort_key
	`)
	if err != nil {
		t.Fatalf("Failed to query visits: %v", err)
	}
	for rows.Next() {
		var path string
		var depth int
		var sortKey []string
		var isExit bool
		rows.Scan(&path, &depth, &sortKey, &isExit)
		t.Logf("  path=%s depth=%d is_exit=%v sort_key=%v", path, depth, isExit, sortKey)
	}
	rows.Close()

	// Query the test plan directly
	t.Log("Test plan from pgmi_test_plan():")
	rows, err = conn.Query(ctx, `SELECT ordinal, step_type, script_path, directory, depth FROM pg_temp.pgmi_test_plan()`)
	if err != nil {
		t.Fatalf("Failed to query test plan: %v", err)
	}
	defer rows.Close()

	var teardownOrder []int
	for rows.Next() {
		var ordinal, depth int
		var stepType, dir string
		var scriptPath *string
		if err := rows.Scan(&ordinal, &stepType, &scriptPath, &dir, &depth); err != nil {
			t.Fatalf("Failed to scan: %v", err)
		}
		pathStr := "<nil>"
		if scriptPath != nil {
			pathStr = *scriptPath
		}
		t.Logf("  ordinal=%d type=%s path=%s dir=%s depth=%d", ordinal, stepType, pathStr, dir, depth)

		if stepType == "teardown" {
			teardownOrder = append(teardownOrder, depth)
		}
	}

	t.Logf("Teardown depths in plan order: %v", teardownOrder)

	// Expected teardown order: 2 (grandchild), 1 (child), 0 (root)
	expected := []int{2, 1, 0}
	for i, d := range teardownOrder {
		if i < len(expected) && d != expected[i] {
			t.Errorf("Teardown[%d] depth=%d, expected %d", i, d, expected[i])
		}
	}
}

func createDebugNestedProject(t *testing.T, projectPath string) {
	t.Helper()

	rootPath := filepath.Join(projectPath, "__test__")
	childPath := filepath.Join(rootPath, "child")
	grandchildPath := filepath.Join(childPath, "grandchild")

	for _, dir := range []string{rootPath, childPath, grandchildPath} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create %s: %v", dir, err)
		}
	}

	// Root
	os.WriteFile(filepath.Join(rootPath, "_setup.sql"), []byte(`SELECT 'root_setup';`), 0644)
	os.WriteFile(filepath.Join(rootPath, "test_root.sql"), []byte(`SELECT 'root_test';`), 0644)

	// Child
	os.WriteFile(filepath.Join(childPath, "_setup.sql"), []byte(`SELECT 'child_setup';`), 0644)
	os.WriteFile(filepath.Join(childPath, "test_child.sql"), []byte(`SELECT 'child_test';`), 0644)

	// Grandchild
	os.WriteFile(filepath.Join(grandchildPath, "_setup.sql"), []byte(`SELECT 'grandchild_setup';`), 0644)
	os.WriteFile(filepath.Join(grandchildPath, "test_deep.sql"), []byte(`SELECT 'grandchild_test';`), 0644)

	// Empty deploy.sql (we won't execute it)
	os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(`SELECT 1;`), 0644)
}

