package scaffold_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// TestDeploy_PreservesOriginalSQLSTATE pins the deploy.sql error-wrapper fix:
// when a migration fails, the wrapper must re-raise with the ORIGINAL SQLSTATE
// and DETAIL, not flatten everything to P0001. A caller (or a retry classifier —
// this is the deploy-time half of the serialization-failure contract) cannot
// tell a retryable 40001 from a permanent 23505 if every failure reports P0001.
func TestDeploy_PreservesOriginalSQLSTATE(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	ctx := context.Background()

	projectPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectPath, "migrations"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A migration that raises a real, classifiable error: unique_violation (23505),
	// which also carries a DETAIL line ("Key (email)=(x) already exists.").
	mig := `
CREATE TABLE dupe_probe (email text UNIQUE);
INSERT INTO dupe_probe VALUES ('x@example.com');
INSERT INTO dupe_probe VALUES ('x@example.com');
`
	if err := os.WriteFile(filepath.Join(projectPath, "migrations", "001.sql"), []byte(mig), 0644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	// Minimal deploy.sql using the same wrapper shape the templates ship.
	deploySQL := `
BEGIN;
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    )
    LOOP
        BEGIN
            EXECUTE v_file.content;
        EXCEPTION WHEN OTHERS THEN
            DECLARE
                v_sqlstate text;
                v_detail   text;
            BEGIN
                GET STACKED DIAGNOSTICS
                    v_sqlstate = RETURNED_SQLSTATE,
                    v_detail   = PG_EXCEPTION_DETAIL;
                RAISE EXCEPTION 'Failed in %: %', v_file.path, SQLERRM
                    USING ERRCODE = v_sqlstate, DETAIL = v_detail;
            END;
        END;
    END LOOP;
END $$;
COMMIT;
`
	if err := os.WriteFile(filepath.Join(projectPath, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("write deploy.sql: %v", err)
	}

	testDB := "pgmi_sqlstate_probe"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	deployer := testhelpers.NewTestDeployer(t)
	err := deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        testDB,
		SourcePath:          projectPath,
		Overwrite:           true,
		Force:               true,
	})
	if err == nil {
		t.Fatal("expected the duplicate-insert migration to fail the deploy")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected a *pgconn.PgError in the chain, got: %v", err)
	}

	// The whole point: the wrapper preserved the original class instead of P0001.
	if pgErr.Code != "23505" {
		t.Errorf("deploy error SQLSTATE = %q, want 23505 (a bare RAISE would give P0001)", pgErr.Code)
	}
	if pgErr.Detail == "" {
		t.Error("the original DETAIL line must survive the wrapper, got empty")
	}
}
