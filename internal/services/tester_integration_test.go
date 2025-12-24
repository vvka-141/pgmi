package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/services"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestDeploymentService_ExecuteTests_AllPass(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	service := testhelpers.NewTestDeployer(t).(*services.DeploymentService)

	// Create project with passing tests
	projectPath := t.TempDir()
	createProjectWithPassingTests(t, projectPath)

	testDB := "pgmi_test_execute_tests_pass"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy first (setup database)
	err := service.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Now run tests
	err = service.ExecuteTests(ctx, pgmi.TestConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		FilterPattern:    ".*",
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Expected tests to pass, got error: %v", err)
	}
}

func TestDeploymentService_ExecuteTests_FailFast(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	service := testhelpers.NewTestDeployer(t).(*services.DeploymentService)

	// Create project with failing test
	projectPath := t.TempDir()
	createProjectWithFailingTest(t, projectPath)

	testDB := "pgmi_test_execute_tests_fail"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy first
	err := service.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Run tests - should fail
	err = service.ExecuteTests(ctx, pgmi.TestConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		FilterPattern:    ".*",
		Verbose:          testing.Verbose(),
	})

	if err == nil {
		t.Fatal("Expected tests to fail, but got no error")
	}

	// Verify error message contains test context
	if !strings.Contains(err.Error(), "test failed") {
		t.Errorf("Expected error to mention 'test failed', got: %v", err)
	}
}

func TestDeploymentService_ExecuteTests_FilterPattern(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	service := testhelpers.NewTestDeployer(t).(*services.DeploymentService)

	// Create project with multiple test directories
	projectPath := t.TempDir()
	createProjectWithMultipleTestDirs(t, projectPath)

	testDB := "pgmi_test_execute_tests_filter"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy first
	err := service.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Run only auth tests
	err = service.ExecuteTests(ctx, pgmi.TestConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		FilterPattern:    "/auth/",
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Auth tests failed: %v", err)
	}
}

func TestDeploymentService_ExecuteTests_ListMode(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	service := testhelpers.NewTestDeployer(t).(*services.DeploymentService)

	// Create project with tests
	projectPath := t.TempDir()
	createProjectWithPassingTests(t, projectPath)

	testDB := "pgmi_test_execute_tests_list"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	// Deploy first
	err := service.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		Overwrite:        true,
		Force:            true,
		Verbose:          testing.Verbose(),
	})
	if err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	// Run in list mode (should not fail)
	err = service.ExecuteTests(ctx, pgmi.TestConfig{
		ConnectionString:    connString,
		DatabaseName:     testDB,
		SourcePath:       projectPath,
		FilterPattern:    ".*",
		ListOnly:         true,
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("List mode failed: %v", err)
	}
}

// Helper functions

func createProjectWithPassingTests(t *testing.T, projectPath string) {
	t.Helper()

	// Create schema
	schema := `CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);`
	err := os.WriteFile(filepath.Join(projectPath, "schema.sql"), []byte(schema), 0644)
	if err != nil {
		t.Fatalf("Failed to create schema.sql: %v", err)
	}

	// Create deploy.sql (simple schema deployment)
	deploySQL := `
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN
        SELECT content
        FROM pg_temp.pgmi_source
        WHERE path = './schema.sql'
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
`
	err = os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}

	// Create test directory
	testPath := filepath.Join(projectPath, "__test__")
	err = os.MkdirAll(testPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	// Create passing test
	test := `
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'users') THEN
        RAISE EXCEPTION 'users table not found';
    END IF;
    RAISE NOTICE '✓ users table exists';
END $$;
`
	err = os.WriteFile(filepath.Join(testPath, "test_users.sql"), []byte(test), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_users.sql: %v", err)
	}
}

func createProjectWithFailingTest(t *testing.T, projectPath string) {
	t.Helper()

	// Create deploy.sql (minimal)
	deploySQL := `SELECT 1;`
	err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}

	// Create test directory
	testPath := filepath.Join(projectPath, "__test__")
	err = os.MkdirAll(testPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create __test__ directory: %v", err)
	}

	// Create failing test
	test := `
DO $$
BEGIN
    RAISE EXCEPTION 'Intentional test failure';
END $$;
`
	err = os.WriteFile(filepath.Join(testPath, "test_fail.sql"), []byte(test), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_fail.sql: %v", err)
	}
}

func createProjectWithMultipleTestDirs(t *testing.T, projectPath string) {
	t.Helper()

	// Create deploy.sql
	deploySQL := `SELECT 1;`
	err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644)
	if err != nil {
		t.Fatalf("Failed to create deploy.sql: %v", err)
	}

	// Create auth tests
	authPath := filepath.Join(projectPath, "__test__", "auth")
	err = os.MkdirAll(authPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create auth test directory: %v", err)
	}
	authTest := `DO $$ BEGIN RAISE NOTICE 'Auth test'; END $$;`
	err = os.WriteFile(filepath.Join(authPath, "test_login.sql"), []byte(authTest), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_login.sql: %v", err)
	}

	// Create billing tests
	billingPath := filepath.Join(projectPath, "__test__", "billing")
	err = os.MkdirAll(billingPath, 0755)
	if err != nil {
		t.Fatalf("Failed to create billing test directory: %v", err)
	}
	billingTest := `DO $$ BEGIN RAISE NOTICE 'Billing test'; END $$;`
	err = os.WriteFile(filepath.Join(billingPath, "test_stripe.sql"), []byte(billingTest), 0644)
	if err != nil {
		t.Fatalf("Failed to create test_stripe.sql: %v", err)
	}
}
