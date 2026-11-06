package services_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A real server one release below the floor is refused with exit 10 and a
// message naming the floor, before any session work.
func TestDeploy_RefusesAServerBelowTheFloor(t *testing.T) {
	connString := testhelpers.RequirePostgresImage(t, "postgres:14")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := testhelpers.NewTestDeployer(t).Deploy(context.Background(), pgmi.DeploymentConfig{
		ConnectionString:    connString,
		MaintenanceDatabase: "postgres",
		DatabaseName:        "postgres",
		SourcePath:          dir,
		Force:               true,
	})
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Fatalf("exit %d, want %d (invalid configuration): %v", got, pgmi.ExitConfigError, err)
	}
	if err == nil || !strings.Contains(err.Error(), "pgmi requires 15 or newer") {
		t.Fatalf("the refusal must name the floor, got: %v", err)
	}
}
