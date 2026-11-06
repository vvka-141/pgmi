package scaffold_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A session PGOPTIONS made read-only refuses pgmi's own temp tables. pgmi got
// as far as the database, so the exit is 13, not the general 1.
func TestSessionPreparationRefusedByTheServerExits13(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	projectPath := filepath.Join(t.TempDir(), "proj")
	if err := scaffold.NewScaffolder(false).CreateProject("proj", "minimal", projectPath); err != nil {
		t.Fatal(err)
	}
	const db = "pgmi_readonly_session"
	testhelpers.CleanupTestDB(t, connString, db)
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, db) })
	if _, err := testhelpers.GetTestPool(t, connString, "postgres").Exec(context.Background(), "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PGOPTIONS", "-c default_transaction_read_only=on")
	err := testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        db,
		SourcePath:          projectPath,
		Force:               true,
	})
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitExecutionFailed {
		t.Fatalf("exit %d, want %d: %v", got, pgmi.ExitExecutionFailed, err)
	}
}
