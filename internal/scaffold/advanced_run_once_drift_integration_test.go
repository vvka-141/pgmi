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

const driftProbeID = "ffffffff-0446-4000-8000-000000000010"

func driftProbe(sortKey, body string) string {
	return fmt.Sprintf(`/*
<pgmi-meta id="%s" idempotent="false">
  <sortKeys><key>%s</key></sortKeys>
</pgmi-meta>
*/
%s`, driftProbeID, sortKey, body)
}

const driftProbeBody = "CREATE TABLE core.drift_probe (id int);\nSELECT pg_temp.exempt_from_rls('core.drift_probe', 'test probe');\n"

// A run-once file is skipped once it has run, so an edit, a move, a new sort key
// or a removal would silently reach only fresh databases. The advanced
// deploy.sql refuses such a deploy before any file runs and names the file.
func TestAdvancedTemplate_RefusesRunOnceDrift(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	original := map[string]string{"core/drift_probe.sql": driftProbe("900/000", driftProbeBody)}

	tests := []struct {
		name    string
		second  map[string]string
		refused bool
		wantMsg []string
	}{
		{
			name:    "DDL edited",
			second:  map[string]string{"core/drift_probe.sql": driftProbe("900/000", "CREATE TABLE core.drift_probe (id int, note text);\nSELECT pg_temp.exempt_from_rls('core.drift_probe', 'test probe');\n")},
			refused: true,
			wantMsg: []string{"./core/drift_probe.sql: content edited"},
		},
		{
			name:    "moved",
			second:  map[string]string{"core/moved/drift_probe.sql": driftProbe("900/000", driftProbeBody)},
			refused: true,
			wantMsg: []string{"./core/moved/drift_probe.sql: moved from ./core/drift_probe.sql"},
		},
		{
			name:    "re-sorted",
			second:  map[string]string{"core/drift_probe.sql": driftProbe("900/500", driftProbeBody)},
			refused: true,
			wantMsg: []string{"./core/drift_probe.sql: sort key 900/500, ran at 900/000"},
		},
		{
			name:    "removed",
			second:  map[string]string{},
			refused: true,
			wantMsg: []string{"./core/drift_probe.sql: removed from the project"},
		},
		{
			name:    "comment, whitespace and keyword case edit",
			second:  map[string]string{"core/drift_probe.sql": driftProbe("900/000", "-- the probe table\ncreate   table core.drift_probe\n    (id int); /* unchanged */\nselect pg_temp.exempt_from_rls('core.drift_probe', 'test probe');\n")},
			refused: false,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := filepath.Join(t.TempDir(), "drift")
			if err := scaffold.NewScaffolder(false).CreateProject("drift", "advanced", projectPath); err != nil {
				t.Fatalf("scaffold the advanced template: %v", err)
			}
			writeProbes(t, projectPath, original, nil)

			testDB := fmt.Sprintf("pgmi_advanced_drift_%d", i)
			defer testhelpers.CleanupTestDB(t, connString, testDB)

			deployer := testhelpers.NewTestDeployer(t)
			deploy := func(overwrite bool) error {
				return deployer.Deploy(context.Background(), pgmi.DeploymentConfig{
					ConnectionString:    connString,
					MaintenanceDatabase: "postgres",
					DatabaseName:        testDB,
					SourcePath:          projectPath,
					Overwrite:           overwrite,
					Force:               true,
					Parameters:          map[string]string{"env": "dev"},
				})
			}

			if err := deploy(true); err != nil {
				t.Fatalf("first deploy: %v", err)
			}

			// A new run-once file in the same deploy shows whether anything from it was applied.
			second := map[string]string{"core/drift_marker.sql": `/*
<pgmi-meta id="ffffffff-0446-4000-8000-000000000011" idempotent="false">
  <sortKeys><key>005/000</key></sortKeys>
</pgmi-meta>
*/
CREATE TABLE core.drift_marker (id int);
SELECT pg_temp.exempt_from_rls('core.drift_marker', 'test probe');
`}
			for k, v := range tt.second {
				second[k] = v
			}
			writeProbes(t, projectPath, second, original)

			err := deploy(false)
			pool := testhelpers.GetTestPool(t, connString, testDB)
			var markerApplied bool
			if qerr := pool.QueryRow(context.Background(),
				"SELECT to_regclass('core.drift_marker') IS NOT NULL").Scan(&markerApplied); qerr != nil {
				t.Fatalf("look for the marker table: %v", qerr)
			}

			if !tt.refused {
				if err != nil {
					t.Fatalf("a comment and whitespace edit refused the deploy: %v", err)
				}
				var runs int
				if qerr := pool.QueryRow(context.Background(), `
					SELECT count(*) FROM internal.deployment_script_execution_log
					WHERE deployment_script_object_id = $1`, driftProbeID).Scan(&runs); qerr != nil {
					t.Fatalf("read the execution log: %v", qerr)
				}
				if runs != 1 {
					t.Errorf("the probe ran %d times, want 1", runs)
				}
				return
			}

			if err == nil {
				t.Fatal("deploy succeeded after a run-once file drifted")
			}
			for _, want := range tt.wantMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("want the error to contain %q, got: %v", want, err)
				}
			}
			if markerApplied {
				t.Error("the refused deploy applied a file: core.drift_marker exists")
			}
		})
	}
}

func writeProbes(t *testing.T, projectPath string, files, previous map[string]string) {
	t.Helper()
	for rel := range previous {
		if err := os.Remove(filepath.Join(projectPath, rel)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for rel, content := range files {
		path := filepath.Join(projectPath, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
