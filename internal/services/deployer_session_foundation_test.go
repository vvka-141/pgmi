package services_test

import (
	"context"
	_ "embed"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/internal/files/loader"
	"github.com/vvka-141/pgmi/internal/files/scanner"
	"github.com/vvka-141/pgmi/internal/params"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/internal/testing/fixtures"
)

//go:embed testdata/session_foundation_test.sql
var sessionFoundationTestSQL string

// TestSessionPreparation_Foundation validates complete session initialization
// with a multi-level project structure. This test:
//
// 1. Creates a mock filesystem with 3+ levels of test directories
// 2. Scans files using the Scanner with mock filesystem
// 3. Runs prepareSession() to initialize all pg_temp tables
// 4. Executes SQL validation suite to verify post-conditions
//
// The test validates:
// - Files loaded correctly into pg_temp.pgmi_source
// - Test files separated into pg_temp.pgmi_unittest_script (then dropped)
// - Execution plan materialized in pg_temp.pgmi_unittest_plan
// - Multi-level directory traversal produces correct execution order
//
// This is a SQL-first test: validation logic lives in SQL, not Go.
func TestSessionPreparation_Foundation(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	// Create multi-level fixture with StandardMultiLevel() fixture
	// Structure:
	//   migrations/001_schema.sql, 002_data.sql
	//   setup/functions.sql
	//   pgitest/_setup.sql, test_basic.sql
	//   pgitest/auth/_setup.sql, test_login.sql
	//   pgitest/auth/oauth/test_google.sql
	//   pgitest/billing/test_stripe.sql
	fs := fixtures.StandardMultiLevel()

	// Create scanner with mock filesystem
	calculator := checksum.New()
	fileScanner := scanner.NewScannerWithFS(calculator, fs)

	// Scan files (excludes deploy.sql)
	scanResult, err := fileScanner.ScanDirectory("/")
	if err != nil {
		t.Fatalf("Failed to scan fixture: %v", err)
	}

	t.Logf("Scanned %d files from fixture", len(scanResult.Files))

	// Create test database
	testDB := "pgmi_test_session_foundation"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	// Get connection pool
	pool := testhelpers.GetTestPool(t, connString, testDB)

	// Acquire connection for session-scoped operations
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	// Prepare session (this is what we're testing)
	// This involves:
	// 1. Create schema.sql objects (pgmi_source, pgmi_parameter, helper functions)
	// 2. Load files into pgmi_source
	// 3. Load parameters into pgmi_parameter
	// 4. Create unittest.sql objects (moves test files, materializes plan)
	t.Log("Creating pg_temp schema and helper functions...")
	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	t.Log("Loading files into pg_temp.pgmi_source...")
	fileLoader := loader.NewLoader()
	if err := fileLoader.LoadFilesIntoSession(ctx, conn, scanResult.Files); err != nil {
		t.Fatalf("Failed to load files: %v", err)
	}

	t.Log("Loading parameters into pg_temp.pgmi_parameter...")
	testParams := map[string]string{
		"env":     "test",
		"version": "1.0.0",
	}
	if err := fileLoader.LoadParametersIntoSession(ctx, conn, testParams); err != nil {
		t.Fatalf("Failed to load parameters: %v", err)
	}

	t.Log("Creating unittest framework (separates test files, materializes plan)...")
	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	// Session preparation complete. Now validate with SQL test suite.
	t.Log("Executing SQL validation suite...")
	t.Log("========================================")

	_, err = conn.Exec(ctx, sessionFoundationTestSQL)
	if err != nil {
		t.Fatalf("Session validation failed: %v", err)
	}

	t.Log("========================================")
	t.Log("✓ All session foundation tests passed")
}

// TestSessionPreparation_EmptyProject validates session initialization with minimal project
func TestSessionPreparation_EmptyProject(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	// Create empty project (only deploy.sql)
	fs := fixtures.EmptyProject()

	calculator := checksum.New()
	fileScanner := scanner.NewScannerWithFS(calculator, fs)

	scanResult, err := fileScanner.ScanDirectory("/")
	if err != nil {
		t.Fatalf("Failed to scan fixture: %v", err)
	}

	t.Logf("Scanned %d files from empty project", len(scanResult.Files))

	testDB := "pgmi_test_session_empty"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	pool := testhelpers.GetTestPool(t, connString, testDB)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	// Prepare session with empty project
	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	fileLoader := loader.NewLoader()
	if err := fileLoader.LoadFilesIntoSession(ctx, conn, scanResult.Files); err != nil {
		t.Fatalf("Failed to load files: %v", err)
	}

	if err := fileLoader.LoadParametersIntoSession(ctx, conn, map[string]string{}); err != nil {
		t.Fatalf("Failed to load parameters: %v", err)
	}

	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	// Verify empty state
	var fileCount, testCount int
	err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM pg_temp.pgmi_source").Scan(&fileCount)
	if err != nil {
		t.Fatalf("Failed to count files: %v", err)
	}

	err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM pg_temp.pgmi_unittest_plan").Scan(&testCount)
	if err != nil {
		t.Fatalf("Failed to count tests: %v", err)
	}

	if fileCount != 0 {
		t.Errorf("Expected 0 files in pgmi_source, got %d", fileCount)
	}

	if testCount != 0 {
		t.Errorf("Expected 0 tests in pgmi_unittest_plan, got %d", testCount)
	}

	t.Log("✓ Empty project session initialized correctly")
}

// TestSessionPreparation_OnlyMigrations validates session with migrations but no tests
func TestSessionPreparation_OnlyMigrations(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	fs := fixtures.OnlyMigrations()

	calculator := checksum.New()
	fileScanner := scanner.NewScannerWithFS(calculator, fs)

	scanResult, err := fileScanner.ScanDirectory("/")
	if err != nil {
		t.Fatalf("Failed to scan fixture: %v", err)
	}

	testDB := "pgmi_test_session_migrations"
	cleanup := testhelpers.CreateTestDB(t, connString, testDB)
	defer cleanup()

	pool := testhelpers.GetTestPool(t, connString, testDB)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Failed to acquire connection: %v", err)
	}
	defer conn.Release()

	// Prepare session
	if err := params.CreateSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	fileLoader := loader.NewLoader()
	if err := fileLoader.LoadFilesIntoSession(ctx, conn, scanResult.Files); err != nil {
		t.Fatalf("Failed to load files: %v", err)
	}

	if err := fileLoader.LoadParametersIntoSession(ctx, conn, map[string]string{}); err != nil {
		t.Fatalf("Failed to load parameters: %v", err)
	}

	if err := params.CreateUnittestSchema(ctx, conn); err != nil {
		t.Fatalf("Failed to create unittest schema: %v", err)
	}

	// Verify: migrations in pgmi_source, no tests
	var migrationCount, testCount int
	err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM pg_temp.pgmi_source WHERE directory ~ 'migrations'").Scan(&migrationCount)
	if err != nil {
		t.Fatalf("Failed to count migrations: %v", err)
	}

	err = conn.QueryRow(ctx, "SELECT COUNT(*) FROM pg_temp.pgmi_unittest_plan").Scan(&testCount)
	if err != nil {
		t.Fatalf("Failed to count tests: %v", err)
	}

	if migrationCount != 2 {
		t.Errorf("Expected 2 migration files, got %d", migrationCount)
	}

	if testCount != 0 {
		t.Errorf("Expected 0 tests, got %d", testCount)
	}

	t.Log("✓ Migrations-only project session initialized correctly")
}
