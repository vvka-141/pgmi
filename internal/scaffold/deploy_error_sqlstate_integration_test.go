package scaffold_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// The scaffolded basic template, with one extra failing migration, must report
// the migration's original SQLSTATE and DETAIL and name the failing file and
// line. A retry classifier cannot tell a retryable 40001 from a permanent 23505
// if every failure reports P0001, and a failure that names neither file nor
// line sends the user reading every migration (PGMI-376, PGMI-339).
func TestBasicTemplate_FailingMigrationKeepsSQLSTATEAndLocation(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	tests := []struct {
		name      string
		migration string
		sqlstate  string
		line      int
	}{
		{
			name:      "runtime error keeps SQLSTATE and DETAIL",
			migration: "CREATE TABLE dupe_probe (email text UNIQUE);\nINSERT INTO dupe_probe VALUES ('x@example.com');\nINSERT INTO dupe_probe VALUES ('x@example.com');\n",
			sqlstate:  "23505",
		},
		{
			name:      "syntax error resolves to the line in the file",
			migration: "CREATE TABLE syntax_probe (id int);\n\n-- line 3\nSELEC 1;\n",
			sqlstate:  "42601",
			line:      4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "proj")
			if err := scaffold.NewScaffolder(false).CreateProject("proj", "basic", projectPath); err != nil {
				t.Fatalf("scaffold: %v", err)
			}
			const failing = "./migrations/999_failing.sql"
			if err := os.WriteFile(filepath.Join(projectPath, "migrations", "999_failing.sql"), []byte(tt.migration), 0o644); err != nil {
				t.Fatalf("write migration: %v", err)
			}

			testDB := "pgmi_sqlstate_probe"
			defer testhelpers.CleanupTestDB(t, connString, testDB)

			err := testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
				ConnectionString:    connString,
				MaintenanceDatabase: "postgres",
				DatabaseName:        testDB,
				SourcePath:          projectPath,
				Overwrite:           true,
				Force:               true,
			})
			if err == nil {
				t.Fatal("expected the failing migration to fail the deploy")
			}

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("expected a *pgconn.PgError in the chain, got: %v", err)
			}
			if pgErr.Code != tt.sqlstate {
				t.Errorf("SQLSTATE = %q, want %q", pgErr.Code, tt.sqlstate)
			}
			if tt.sqlstate == "23505" && pgErr.Detail == "" {
				t.Error("the original DETAIL line was lost")
			}

			d := pgmi.NewErrorDetail(err)
			if d.FailedFile != failing {
				t.Errorf("failedFile = %q, want %q\n%s", d.FailedFile, failing, pgmi.FormatError(err))
			}
			if tt.line > 0 && (d.Script != failing || d.Line != tt.line) {
				t.Errorf("location = %s line %d, want %s line %d", d.Script, d.Line, failing, tt.line)
			}
		})
	}
}
