package services_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	testhelpers "github.com/vvka-141/pgmi/internal/testing"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// --compat is resolved entirely offline, but it used to be validated only once
// the session was being prepared -- after --overwrite had already dropped and
// recreated the database. An operator who fat-fingered the version lost the
// database to a check that never needed the server.
func TestDeploy_UnsupportedCompat_RejectedBeforeOverwriteDropsTheDatabase(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	ctx := context.Background()
	const dbName = "pgmi220_compat_guard"

	adminPool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer adminPool.Close()

	// Terminate-then-drop rather than WITH (FORCE): the latter is PostgreSQL 13+,
	// and the suite has to run on the version floor the README documents.
	if _, err := adminPool.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName); err != nil {
		t.Fatalf("terminate connections to the pre-existing database: %v", err)
	}
	if _, err := adminPool.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName); err != nil {
		t.Fatalf("drop pre-existing database: %v", err)
	}
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { testhelpers.CleanupTestDB(t, connString, dbName) })

	targetPool := testhelpers.GetTestPool(t, connString, dbName)
	if _, err := targetPool.Exec(ctx, "CREATE TABLE survivor(id int)"); err != nil {
		t.Fatalf("seed target database: %v", err)
	}
	if _, err := targetPool.Exec(ctx, "INSERT INTO survivor VALUES (42)"); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	targetPool.Close()

	projectPath := t.TempDir()
	createMinimalProject(t, projectPath)

	deployer := testhelpers.NewTestDeployer(t)
	err = deployer.Deploy(ctx, pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        dbName,
		SourcePath:          projectPath,
		Compat:              "99",
		Overwrite:           true,
		Force:               true,
		Verbose:             testing.Verbose(),
	})
	if err == nil {
		t.Fatal("expected an unsupported --compat version to fail the deploy")
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitConfigError, err)
	}

	survivorPool := testhelpers.GetTestPool(t, connString, dbName)
	defer survivorPool.Close()

	var id int
	if err := survivorPool.QueryRow(ctx, "SELECT id FROM survivor").Scan(&id); err != nil {
		t.Fatalf("the database was dropped before --compat was validated: %v", err)
	}
	if id != 42 {
		t.Errorf("survivor row = %d, want 42", id)
	}
}
