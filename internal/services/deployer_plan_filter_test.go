package services_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestPlanView_IsSQLFileGuardExcludesNonSQL runs the loop the embedded docs
// teach against a project holding the debris a real repository accumulates: an
// editor backup of a migration and a README. Without the is_sql_file guard the
// backup executes as a migration and the deploy still reports success, which is
// the worst failure mode a deployment tool has.
func TestPlanView_IsSQLFileGuardExcludesNonSQL(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	const guardedLoop = `
BEGIN;
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT p.path, p.content FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
`

	// Same loop, guard removed — the shape the docs used to teach.
	const unguardedLoop = `
BEGIN;
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
`

	tests := []struct {
		name          string
		deploySQL     string
		wantBackupRan bool
	}{
		{name: "guarded loop skips the backup", deploySQL: guardedLoop, wantBackupRan: false},
		{name: "unguarded loop executes the backup", deploySQL: unguardedLoop, wantBackupRan: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			projectPath := t.TempDir()

			migrations := filepath.Join(projectPath, "migrations")
			if err := os.MkdirAll(migrations, 0755); err != nil {
				t.Fatalf("Failed to create migrations dir: %v", err)
			}

			files := map[string]string{
				filepath.Join(migrations, "001_init.sql"):  "CREATE TABLE IF NOT EXISTS good_table(id int);",
				filepath.Join(migrations, "001_init.sql~"): "CREATE TABLE IF NOT EXISTS stale_backup_leaked(id int);",
				filepath.Join(projectPath, "README.md"):    "# Project\n\nNot SQL.\n",
				filepath.Join(projectPath, "deploy.sql"):   tt.deploySQL,
			}
			for path, content := range files {
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatalf("Failed to write %s: %v", path, err)
				}
			}

			testDB := "pgmi_plan_filter"
			defer testhelpers.CleanupTestDB(t, connString, testDB)

			err := testhelpers.NewTestDeployer(t).Deploy(ctx, pgmi.DeploymentConfig{
				ConnectionString:    connString,
				MaintenanceDatabase: "postgres",
				DatabaseName:        testDB,
				SourcePath:          projectPath,
				Overwrite:           true,
				Force:               true,
				Verbose:             testing.Verbose(),
			})
			if err != nil {
				t.Fatalf("deployment failed: %v", err)
			}

			pool := testhelpers.GetTestPool(t, connString, testDB)

			var goodExists, backupExists bool
			if err := pool.QueryRow(ctx, `SELECT to_regclass('public.good_table') IS NOT NULL`).Scan(&goodExists); err != nil {
				t.Fatalf("querying good_table: %v", err)
			}
			if err := pool.QueryRow(ctx, `SELECT to_regclass('public.stale_backup_leaked') IS NOT NULL`).Scan(&backupExists); err != nil {
				t.Fatalf("querying stale_backup_leaked: %v", err)
			}

			if !goodExists {
				t.Error("the real migration did not run — the loop is broken, not merely unfiltered")
			}
			if backupExists != tt.wantBackupRan {
				if tt.wantBackupRan {
					t.Error("unguarded loop did NOT execute 001_init.sql~ — this test no longer demonstrates the hazard the guard prevents")
				} else {
					t.Error("is_sql_file guard did not stop 001_init.sql~ from executing as a migration")
				}
			}
		})
	}
}
