package scaffold_test

import (
	"context"
	"testing"

	"github.com/vvka-141/pgmi/internal/files/fakefs"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// core.apply_org_rls and core.apply_change_tracking are documented helpers a
// DBA may call by hand on a deployed database, where no deploy session and so
// no pg_temp helper exists.
func TestAdvancedTemplate_CoreHelpersWorkOutsideADeploy(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()
	const testDB = "pgmi_core_helpers_outside"

	efs := fakefs.NewEmbedFileSystem(scaffold.GetTemplatesFS(), "templates/advanced")
	defer testhelpers.CleanupTestDB(t, connString, testDB)
	if err := testhelpers.NewTestDeployerWithFS(t, efs).Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString: connString,
		DatabaseName:     testDB,
		SourcePath:       ".",
		Overwrite:        true,
		Force:            true,
		Parameters:       map[string]string{"env": "dev"},
	}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	tx, err := testhelpers.GetTestPool(t, connString, testDB).Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		"SET LOCAL ROLE " + testDB + "_owner",
		"CREATE TABLE core.outside_probe (object_id uuid PRIMARY KEY, organization_id uuid NOT NULL, created_by_user_id uuid)",
		"SELECT core.apply_org_rls('core.outside_probe')",
		"SELECT core.apply_change_tracking('core.outside_probe')",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}
