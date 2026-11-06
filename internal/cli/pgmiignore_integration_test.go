package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/files/pathrule"
	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A client build output under clients/ is not text, so loading it fails the
// scan. .pgmiignore keeps it out, and pgmi deploy and the serve deploy tool
// must honour it the same way.
func TestPgmiignore_DeployAndServeAgree(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const testDB = "pgmi_pgmiignore_parity"

	for _, tt := range []struct {
		name       string
		pgmiignore string
		wantExit   int
	}{
		{name: "without .pgmiignore the binary is loaded and refused", wantExit: pgmi.ExitConfigError},
		{name: "with .pgmiignore the folder stays out", pgmiignore: "clients/\n", wantExit: pgmi.ExitSuccess},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearPGEnv(t)
			dir := t.TempDir()
			writeProjectFile(t, dir, "deploy.sql", "SELECT 1;\n")
			if err := os.MkdirAll(filepath.Join(dir, "clients", "csharp", "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeProjectFile(t, dir, "clients/csharp/bin/App.dll", "MZ\x00\x00binary")
			if tt.pgmiignore != "" {
				writeProjectFile(t, dir, pathrule.PgmiignoreFile, tt.pgmiignore)
			}

			testhelpers.CleanupTestDB(t, connString, testDB)
			resetDeployFlags()
			t.Cleanup(resetDeployFlags)
			deployFlags.connection = connString
			deployFlags.database = testDB
			deployFlags.timeout = 3 * time.Minute
			deployFlags.force = true
			cliErr := runDeploy(deployCmd, []string{dir})
			if got := pgmi.ExitCodeForError(cliErr); got != tt.wantExit {
				t.Errorf("pgmi deploy: exit %d, want %d (err: %v)", got, tt.wantExit, cliErr)
			}

			testhelpers.CleanupTestDB(t, connString, testDB)
			result, _ := callTool(t, "deploy", map[string]any{"path": dir, "connection": connString, "database": testDB})
			sc, _ := result["structuredContent"].(map[string]any)
			if got, _ := sc["exitCode"].(float64); int(got) != tt.wantExit {
				t.Errorf("serve deploy tool: exit %v, want %d (%v)", sc["exitCode"], tt.wantExit, sc)
			}
			if n, _ := sc["pathsIgnored"].(float64); tt.wantExit == pgmi.ExitSuccess && n < 1 {
				t.Errorf("serve deploy result must count the ignored clients/ folder, got pathsIgnored=%v", sc["pathsIgnored"])
			}
			testhelpers.CleanupTestDB(t, connString, testDB)
		})
	}
}
