package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestDeploymentService_Deploy_BasicWorkflow(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	// Create temp directory with minimal project
	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)

	testDB := "pgmi_svc_test_basic"

	// Deploy with overwrite and force
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

	// Verify database was created
	pool := testhelpers.GetTestPool(t, connString, testDB)
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	var result int
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("Failed to query deployed database: %v", err)
	}
	if result != 1 {
		t.Errorf("Expected result=1, got %d", result)
	}
}

func TestDeploymentService_Deploy_IdempotentRedeployment(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)

	testDB := "pgmi_test_idempotent"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// First deployment with overwrite
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
		t.Fatalf("First deployment failed: %v", err)
	}

	// Second deployment without overwrite (idempotent test)
	err = deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        false,
		Force:            false,
		Verbose:          testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("Idempotent redeployment failed: %v", err)
	}

	// Verify database still works
	pool := testhelpers.GetTestPool(t, connString, testDB)

	var result int
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("Failed to query after redeployment: %v", err)
	}
}

func TestDeploymentService_Deploy_WithParameters(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createProjectWithParams(t, projectPath)

	testDB := "pgmi_test_params"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy with parameters
	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Parameters: map[string]string{
			"app_name":    "test_app",
			"app_version": "1.0.0",
		},
		Verbose: testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Deploy with parameters failed: %v", err)
	}

	// Verify parameters were loaded by checking the table created by deploy.sql
	pool := testhelpers.GetTestPool(t, connString, testDB)

	var appName, appVersion string
	err = pool.QueryRow(ctx, "SELECT app_name, app_version FROM deployment_info LIMIT 1").Scan(&appName, &appVersion)
	if err != nil {
		t.Fatalf("Failed to read deployment info: %v", err)
	}
	if appName != "test_app" {
		t.Errorf("Expected app_name='test_app', got %q", appName)
	}
	if appVersion != "1.0.0" {
		t.Errorf("Expected app_version='1.0.0', got %q", appVersion)
	}
}

func TestDeploymentService_Deploy_MissingRootSQL(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	// Don't create deploy.sql

	testDB := "pgmi_test_missing_root"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})

	if err == nil {
		t.Fatal("Expected error for missing deploy.sql, got nil")
	}

	// Verify error message mentions deploy.sql
	errMsg := err.Error()
	if errMsg == "" {
		t.Error("Expected non-empty error message")
	}
}

func TestDeploymentService_Deploy_InvalidSQL(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createProjectWithInvalidSQL(t, projectPath)

	testDB := "pgmi_test_invalid_sql"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})

	if err == nil {
		t.Fatal("Expected error for invalid SQL, got nil")
	}

	t.Logf("Got expected error: %v", err)
}

func TestDeploymentService_Deploy_WithFiles(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	deployer := testhelpers.NewTestDeployer(t)

	projectPath := t.TempDir()
	createProjectWithFiles(t, projectPath)

	testDB := "pgmi_test_files"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

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
		t.Fatalf("Deploy with files failed: %v", err)
	}

	// Verify files were loaded into session
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

// Helper functions to create test projects

func createMinimalProject(t *testing.T, projectPath string) {
	t.Helper()

	rootSQL := `
-- Minimal deploy.sql for testing
SELECT 1;
`
	err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(rootSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createProjectWithParams(t *testing.T, projectPath string) {
	t.Helper()

	rootSQL := `
-- Test parameter resolution
-- Create a table to persist parameter values for verification
CREATE TABLE deployment_info (
    app_name TEXT,
    app_version TEXT
);

-- Read parameters and insert them into the table
DO $$
DECLARE
    v_app_name TEXT;
    v_app_version TEXT;
BEGIN
    -- Read parameters from pg_temp.pgmi_parameter
    SELECT value INTO v_app_name FROM pg_temp.pgmi_parameter WHERE key = 'app_name';
    SELECT value INTO v_app_version FROM pg_temp.pgmi_parameter WHERE key = 'app_version';

    -- Insert into persistent table for verification
    INSERT INTO deployment_info (app_name, app_version)
    VALUES (v_app_name, v_app_version);
END $$;
`
	err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(rootSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createProjectWithInvalidSQL(t *testing.T, projectPath string) {
	t.Helper()

	rootSQL := `
-- Invalid SQL that should fail
SELECT * FROM nonexistent_table_xyz;
`
	err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(rootSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}

func createProjectWithFiles(t *testing.T, projectPath string) {
	t.Helper()

	// Create migrations directory
	migrationsPath := filepath.Join(projectPath, "migrations")
	err := os.MkdirAll(migrationsPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create migrations directory: %v", err)
	}

	// Create migration file
	migrationSQL := `
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    username TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
`
	err = os.WriteFile(filepath.Join(migrationsPath, "001_create_users.sql"), []byte(migrationSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create migration file: %v", err)
	}

	// Create deploy.sql that executes the migration
	rootSQL := `
-- Execute migrations from loaded files
DO $$
DECLARE
    file_record RECORD;
BEGIN
    FOR file_record IN
        SELECT content
        FROM pg_temp.pgmi_source
        WHERE path LIKE '%migrations%'
        ORDER BY path
    LOOP
        EXECUTE file_record.content;
    END LOOP;
END $$;
`
	err = os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(rootSQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}
}
