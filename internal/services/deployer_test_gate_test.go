package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestTestGate_EmptyPlanFailsTheDeploy covers the failure mode that hides every
// other one: a deploy whose test suite found nothing still reports success, so
// a regression in __test__ discovery is invisible. pgmi_test() expanding to an
// empty plan must abort the deploy instead.
func TestTestGate_EmptyPlanFailsTheDeploy(t *testing.T) {
	tests := []struct {
		name      string
		withTests bool
		call      string
		wantErr   string
	}{
		{
			name:      "no test files at all",
			withTests: false,
			call:      "CALL pgmi_test();",
			wantErr:   "no tests were discovered",
		},
		{
			name:      "pattern matches nothing",
			withTests: true,
			call:      "CALL pgmi_test('no_such_test_anywhere');",
			wantErr:   "no tests matched the requested pattern",
		},
		{
			name:      "tests present and matched",
			withTests: true,
			call:      "CALL pgmi_test();",
			wantErr:   "",
		},
	}

	connString := testhelpers.RequireDatabase(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			deployer := testhelpers.NewTestDeployer(t)

			projectPath := t.TempDir()

			if tt.withTests {
				testDir := filepath.Join(projectPath, "__test__")
				if err := os.MkdirAll(testDir, 0755); err != nil {
					t.Fatalf("Failed to create __test__: %v", err)
				}
				if err := os.WriteFile(
					filepath.Join(testDir, "test_noop.sql"), []byte("SELECT 1;\n"), 0644,
				); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
			}

			deploySQL := "BEGIN;\n" + tt.call + "\nCOMMIT;\n"
			if err := os.WriteFile(
				filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644,
			); err != nil {
				t.Fatalf("Failed to create deploy.sql: %v", err)
			}

			testDB := "pgmi_test_gate"
			defer testhelpers.CleanupTestDB(t, connString, testDB)

			err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
				ConnectionString:    connString,
				MaintenanceDatabase: "postgres",
				DatabaseName:        testDB,
				SourcePath:          projectPath,
				Overwrite:           true,
				Force:               true,
				Verbose:             testing.Verbose(),
			})

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("deploy with discoverable tests should succeed, got: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("deploy succeeded despite running no tests — the gate is open")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error does not explain the empty plan.\n got: %v\nwant substring: %q", err, tt.wantErr)
			}
		})
	}
}
