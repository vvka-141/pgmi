package scaffold_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A run-once file runs once and is skipped forever after, so a function in it
// could never be changed by a later deploy. A re-run file runs on every deploy,
// so a table in it is either re-created or silently skipped when edited, and
// its index DDL locks writers on every redeploy (PGMI-393). An operator or a
// constraint trigger has no OR REPLACE form, so a re-run file holding one fails
// on its second deploy; both count as structure (PGMI-449). The advanced
// deploy.sql refuses both before running anything and names the file.
func TestAdvancedTemplate_RefusesFileOfTheWrongKind(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	tests := []struct {
		name       string
		idempotent bool
		body       string
		wantMsg    string
	}{
		{
			name:       "run-once file defining a function",
			idempotent: false,
			body:       "CREATE TABLE core.kind_probe (id int);\n\nCREATE OR REPLACE FUNCTION core.kind_probe_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;\n",
			wantMsg:    "CREATE OR REPLACE FUNCTION",
		},
		{
			name:       "re-run file creating a table",
			idempotent: true,
			body:       "CREATE OR REPLACE FUNCTION core.kind_probe_fn() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;\n\n  CREATE TABLE core.kind_probe (id int);\n",
			wantMsg:    "CREATE TABLE",
		},
		{
			name:       "re-run file creating an operator",
			idempotent: true,
			body:       "CREATE OR REPLACE FUNCTION core.kind_probe_fn(text, int) RETURNS int LANGUAGE sql AS $$ SELECT $2 $$;\n\nCREATE OPERATOR core.?> (LEFTARG = text, RIGHTARG = int, FUNCTION = core.kind_probe_fn);\n",
			wantMsg:    "CREATE OPERATOR",
		},
		{
			name:       "re-run file creating a constraint trigger",
			idempotent: true,
			body:       "CREATE OR REPLACE FUNCTION core.kind_probe_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$;\n\nCREATE CONSTRAINT TRIGGER kind_probe_tg AFTER INSERT ON core.kind_probe\n    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION core.kind_probe_fn();\n",
			wantMsg:    "CREATE CONSTRAINT TRIGGER",
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "kind")
			if err := scaffold.NewScaffolder(false).CreateProject("kind", "advanced", projectPath); err != nil {
				t.Fatalf("scaffold the advanced template: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(projectPath, "core"), 0o755); err != nil {
				t.Fatal(err)
			}
			probe := fmt.Sprintf(`/*
<pgmi-meta id="ffffffff-0446-4000-8000-00000000000%d" idempotent="%t">
  <sortKeys><key>900/000</key></sortKeys>
</pgmi-meta>
*/
-- CREATE TABLE in a comment is not a statement
%s`, i, tt.idempotent, tt.body)
			if err := os.WriteFile(filepath.Join(projectPath, "core", "kind_probe.sql"), []byte(probe), 0o644); err != nil {
				t.Fatal(err)
			}

			testDB := fmt.Sprintf("pgmi_advanced_kind_%d", i)
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
				t.Fatal("deploy succeeded with a file of the wrong kind")
			}
			if !strings.Contains(err.Error(), "./core/kind_probe.sql") || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("want an error naming ./core/kind_probe.sql and %s, got: %v", tt.wantMsg, err)
			}
		})
	}
}

// A run-once file is logged at its first sort key and skipped at the others,
// while the metadata skill said it runs at each. Several keys are for re-run
// files; the advanced deploy.sql refuses a run-once file with more than one.
func TestAdvancedTemplate_RefusesRunOnceFileWithSeveralSortKeys(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := filepath.Join(t.TempDir(), "keys")
	if err := scaffold.NewScaffolder(false).CreateProject("keys", "advanced", projectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectPath, "core"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := `/*
<pgmi-meta id="ffffffff-0489-4000-8000-000000000001" idempotent="false">
  <sortKeys><key>900/000</key><key>950/000</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.keys_probe (id int);
`
	if err := os.WriteFile(filepath.Join(projectPath, "core", "keys_probe.sql"), []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}

	const testDB = "pgmi_advanced_keys"
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
	if err == nil || !strings.Contains(err.Error(), "Run-once file(s) with more than one sort key: ./core/keys_probe.sql") {
		t.Fatalf("want the two-key run-once file refused by name, got: %v", err)
	}
}
