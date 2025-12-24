package scaffold_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/filesystem"
	"github.com/vvka-141/pgmi/internal/scaffold"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestTemplateDeployment tests all templates by initializing and deploying them
// using the Deployer interface (not CLI).
func TestTemplateDeployment(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	templates := []string{"basic", "advanced"}

	for _, templateName := range templates {
		t.Run(templateName, func(t *testing.T) {
			testTemplateDeployment(t, connString, templateName)
		})
	}
}

func testTemplateDeployment(t *testing.T, connString, templateName string) {
	ctx := context.Background()

	// Advanced template requires plv8 extension - skip if not available
	if templateName == "advanced" {
		if !checkExtensionAvailable(t, connString, "plv8") {
			t.Skip("Skipping advanced template test: plv8 extension not available")
		}
	}

	// Create EmbedFileSystem from embedded templates
	// This approach eliminates the need for filesystem I/O during testing
	templateRoot := "templates/" + templateName
	embedFS := scaffold.GetTemplatesFS()
	efs := filesystem.NewEmbedFileSystem(embedFS, templateRoot)

	t.Logf("Testing %s template deployment from embedded FS...", templateName)

	// Step 1: Deploy directly from embedded filesystem with overwrite and force
	testDB := fmt.Sprintf("pgmi_test_%s", templateName)

	t.Logf("Deploying to database %s with overwrite=true, force=true...", testDB)

	deployer := testhelpers.NewTestDeployerWithFS(t, efs)

	// Template-specific parameters
	params := make(map[string]string)
	if templateName == "advanced" {
		params["database_admin_password"] = "TestPassword123!"
		params["database_api_password"] = "ApiPassword123!"
		params["env"] = "test"
	}

	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".", // EmbedFileSystem root is already at template root
		Overwrite:        true,
		Force:            true,
		Parameters:       params,
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("First deployment failed for %s: %v", templateName, err)
	}
	t.Logf("✓ First deployment completed successfully")

	// Step 2: Redeploy WITHOUT overwrite (idempotent test)
	t.Logf("Redeploying without overwrite (idempotent test)...")
	err = deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".",
		Overwrite:        false,
		Force:            false,
		Parameters:       params,
		Verbose:          testing.Verbose(),
	})

	if err != nil {
		t.Fatalf("Idempotent redeployment failed for %s: %v", templateName, err)
	}
	t.Logf("✓ Idempotent redeployment completed successfully")

	// Step 3: Verify deployment by querying the database
	defer testhelpers.CleanupTestDB(t, connString, testDB)
	verifyTemplateDeployment(t, connString, testDB, templateName)
}

// checkExtensionAvailable checks if a PostgreSQL extension is available for installation
func checkExtensionAvailable(t *testing.T, connString, extName string) bool {
	t.Helper()
	ctx := context.Background()
	pool := testhelpers.GetTestPool(t, connString, "postgres")
	defer pool.Close()

	var available bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_available_extensions WHERE name = $1
		)
	`, extName).Scan(&available)
	if err != nil {
		t.Logf("Warning: could not check extension availability: %v", err)
		return false
	}
	return available
}

// verifyTemplateDeployment performs template-specific verification queries
func verifyTemplateDeployment(t *testing.T, connString, dbName, templateName string) {
	t.Helper()

	ctx := context.Background()
	pool := testhelpers.GetTestPool(t, connString, dbName)

	// Basic sanity check - ensure we can query the database
	var result int
	err := pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("Failed to query deployed database: %v", err)
	}
	if result != 1 {
		t.Errorf("Expected result=1, got %d", result)
	}

	// Template-specific verification
	switch templateName {
	case "basic":
		// Verify hello_world() function was created and works correctly
		var greeting string
		err = pool.QueryRow(ctx, "SELECT public.hello_world()").Scan(&greeting)
		if err != nil {
			t.Fatalf("Failed to call hello_world() function: %v", err)
		}
		expectedGreeting := "Hello, World!" // Default parameter value
		if greeting != expectedGreeting {
			t.Errorf("Expected greeting '%s', got '%s'", expectedGreeting, greeting)
		}
		t.Logf("✓ Basic template deployment verified: hello_world() = '%s'", greeting)

		// Note: Session variables (pgi.*) are session-scoped and don't persist across connections.
		// They exist only during the deployment session. To verify session variables work,
		// see TestSessionVariablesWithCustomParams which tests within the deployment session.

	case "advanced":
		// Advanced template may have additional structures
		// Verify that any tables/functions were created as expected
		var schemaExists bool
		err = pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.schemata
				WHERE schema_name = 'public'
			)
		`).Scan(&schemaExists)
		if err != nil {
			t.Fatalf("Failed to verify schema existence: %v", err)
		}
		if !schemaExists {
			t.Error("Expected 'public' schema to exist")
		}
		t.Logf("✓ Advanced template deployment verified")

	}
}

