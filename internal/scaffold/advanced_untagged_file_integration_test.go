package scaffold_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A SQL file without sortKeys is ordered by its path, and './' sorts before the
// framework's numbered keys, so it ran before the types it used existed and
// failed with an error that never mentioned the missing header. The advanced
// deploy.sql now names such files and stops before running anything.
func TestAdvancedTemplate_RejectsSQLFileWithoutPlanPosition(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	projectPath := filepath.Join(t.TempDir(), "untagged")
	if err := scaffold.NewScaffolder(false).CreateProject("untagged", "advanced", projectPath); err != nil {
		t.Fatalf("scaffold the advanced template: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectPath, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "core", "my_tables.sql"),
		[]byte("CREATE TABLE core.untagged_probe (id core.entity_id);\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const testDB = "pgmi_advanced_untagged"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	err := testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
		Parameters:          map[string]string{"env": "dev"},
	})
	if err == nil {
		t.Fatal("deploy succeeded with a SQL file that has no place in the plan")
	}
	if !strings.Contains(err.Error(), "without a place in the plan") || !strings.Contains(err.Error(), "./core/my_tables.sql") {
		t.Fatalf("want an error naming ./core/my_tables.sql, got: %v", err)
	}
}
