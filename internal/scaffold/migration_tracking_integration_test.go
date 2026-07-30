package scaffold_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestBasicTemplate_OptInMigrationTracking pins both execution models of the
// basic template's deploy.sql.
//
// The difference is only observable with a migration that is NOT idempotent, so
// the fixture appends a row. Re-run twice:
//
//	default (tracking commented out) → the migration runs twice → 2 rows
//	tracking uncommented             → the migration runs once  → 1 row
//
// An idempotent probe would pass under both models and prove nothing.
func TestBasicTemplate_OptInMigrationTracking(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	tests := []struct {
		name         string
		enableTrack  bool
		wantRowsAfr2 int
	}{
		{"default re-runs every migration on every deploy", false, 2},
		{"uncommenting the tracking block applies each migration once", true, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectPath := t.TempDir()
			writeTrackingProject(t, projectPath, tt.enableTrack)

			testDB := "pgmi_track_off"
			if tt.enableTrack {
				testDB = "pgmi_track_on"
			}
			defer testhelpers.CleanupTestDB(t, connString, testDB)

			deployer := testhelpers.NewTestDeployer(t)
			deploy := func(overwrite bool) {
				t.Helper()
				err := deployer.Deploy(context.Background(), pgmi.DeploymentConfig{
					ConnectionString:    connString,
					MaintenanceDatabase: "postgres",
					DatabaseName:        testDB,
					SourcePath:          projectPath,
					Overwrite:           overwrite,
					Force:               overwrite,
				})
				if err != nil {
					t.Fatalf("deploy failed: %v", err)
				}
			}

			deploy(true)  // fresh database
			deploy(false) // second deploy against the same database

			pool := testhelpers.GetTestPool(t, connString, testDB)

			var rows int
			if err := pool.QueryRow(context.Background(),
				"SELECT count(*) FROM applied_probe").Scan(&rows); err != nil {
				t.Fatalf("read probe table: %v", err)
			}
			if rows != tt.wantRowsAfr2 {
				t.Errorf("after two deploys the non-idempotent migration produced %d row(s), want %d",
					rows, tt.wantRowsAfr2)
			}

			// The ledger only exists when the block is uncommented.
			var ledgerExists bool
			if err := pool.QueryRow(context.Background(),
				"SELECT to_regclass('public._migration') IS NOT NULL").Scan(&ledgerExists); err != nil {
				t.Fatalf("probe for _migration: %v", err)
			}
			if ledgerExists != tt.enableTrack {
				t.Errorf("_migration table exists = %v, want %v (the default deploy must not create one)",
					ledgerExists, tt.enableTrack)
			}
		})
	}
}

// writeTrackingProject scaffolds a minimal project using the real basic-template
// deploy.sql, optionally uncommenting the (A)/(B)/(C) tracking lines exactly as a
// user would.
func writeTrackingProject(t *testing.T, projectPath string, enableTracking bool) {
	t.Helper()

	deploySQL, err := os.ReadFile(filepath.Join("templates", "basic", "deploy.sql"))
	if err != nil {
		t.Fatalf("read basic deploy.sql: %v", err)
	}
	sql := string(deploySQL)

	if enableTracking {
		for _, marker := range []string{"-- (A) ", "-- (B) ", "-- (C) "} {
			if !strings.Contains(sql, marker) {
				t.Fatalf("deploy.sql no longer carries the %q tracking marker", strings.TrimSpace(marker))
			}
			sql = strings.ReplaceAll(sql, marker, "")
		}
	}

	// The template's deploy.sql reads project.json and seeds via upsert_user; keep
	// both so this exercises the shipped script rather than a reduced copy.
	sql = strings.ReplaceAll(sql, "CALL pgmi_test();", "-- no tests in this fixture")

	mustWrite(t, filepath.Join(projectPath, "deploy.sql"), sql)
	mustWrite(t, filepath.Join(projectPath, "project.json"),
		`{"app_name": "tracking-probe", "version": "1.0.0"}`)

	if err := os.MkdirAll(filepath.Join(projectPath, "migrations"), 0755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	mustWrite(t, filepath.Join(projectPath, "migrations", "001_schema.sql"), `
CREATE TABLE IF NOT EXISTS applied_probe (id serial PRIMARY KEY);
CREATE OR REPLACE FUNCTION upsert_user(p_email text, p_name text)
RETURNS TABLE (id int, email text) LANGUAGE sql AS $$ SELECT 1, p_email $$;
`)
	// Deliberately NOT idempotent: this is what makes the two models distinguishable.
	mustWrite(t, filepath.Join(projectPath, "migrations", "002_data.sql"),
		"INSERT INTO applied_probe DEFAULT VALUES;\n")
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
