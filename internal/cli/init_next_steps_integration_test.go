package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// The deploy command pgmi init prints for the advanced template must succeed
// exactly as printed on a fresh database (PGMI-408).
func TestNextSteps_PrintedAdvancedCommandDeploys(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	var serverVersion int
	if err := testhelpers.GetTestPool(t, connString, "postgres").QueryRow(context.Background(),
		"SELECT current_setting('server_version_num')::int").Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}
	if serverVersion < 180000 {
		t.Skipf("the advanced template needs PostgreSQL 18; this server is %d (the PostgreSQL-floor CI job)", serverVersion)
	}
	const db = "pgmi_next_steps_advanced"
	dir := filepath.Join(t.TempDir(), "app")
	if err := scaffold.NewScaffolder(false).CreateProject("app", "advanced", dir); err != nil {
		t.Fatal(err)
	}

	var printed string
	for _, line := range strings.Split(nextSteps(dir, db, "advanced"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "pgmi deploy ") {
			printed = strings.TrimSpace(line)
		}
	}
	args := strings.Fields(printed)
	if len(args) < 3 || args[0] != "pgmi" || args[1] != "deploy" {
		t.Fatalf("no deploy command in the next steps: %q", printed)
	}

	clearPGEnv(t)
	t.Setenv("PGMI_CONNECTION_STRING", connString)
	testhelpers.CleanupTestDB(t, connString, db)
	defer testhelpers.CleanupTestDB(t, connString, db)
	resetDeployFlags()
	t.Cleanup(resetDeployFlags)
	if err := deployCmd.ParseFlags(args[2:]); err != nil {
		t.Fatalf("printed command does not parse: %v", err)
	}
	deployFlags.force = true

	if err := runDeploy(deployCmd, deployCmd.Flags().Args()); err != nil {
		t.Fatalf("printed command %q fails on a fresh database (exit %d): %v", printed, pgmi.ExitCodeForError(err), err)
	}
}
