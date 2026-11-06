//go:build unix

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func slowProject(t *testing.T) string {
	t.Helper()
	dir := provenanceProject(t, `BEGIN;
DO $$
DECLARE
    v_file record;
BEGIN
    FOR v_file IN SELECT path, content FROM pg_temp.pgmi_plan_view ORDER BY execution_order LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
`)
	if err := os.MkdirAll(filepath.Join(dir, "semantic"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "semantic", "zz_slow.sql"), []byte("SELECT pg_sleep(10);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func deploySlowJSON(t *testing.T, connString, database string, timeout time.Duration, during func()) (map[string]any, error) {
	t.Helper()
	resetDeployFlags()
	clearPGEnv(t)
	deployFlags.connection = connString
	deployFlags.database = database
	deployFlags.force = true
	deployFlags.jsonOutput = true
	deployFlags.timeout = timeout
	dir := slowProject(t)

	var err error
	out := captureStdout(t, func() {
		done := make(chan struct{})
		defer func() { <-done }()
		go func() {
			defer close(done)
			if during != nil {
				during()
			}
		}()
		err = runDeploy(deployCmd, []string{dir})
	})
	return decodeEnvelope(t, out), err
}

func requireStoppedInSlowFile(t *testing.T, env map[string]any, err error, wantExit int) {
	t.Helper()
	if got := pgmi.ExitCodeForError(err); got != wantExit {
		t.Fatalf("exit code %d, want %d: %v", got, wantExit, err)
	}
	if env["exitCode"] != float64(wantExit) || env["sqlstate"] != "57014" ||
		env["failedFile"] != "./semantic/zz_slow.sql" || env["phase"] != "deploy" {
		t.Errorf("--json record = exitCode %v sqlstate %v failedFile %v phase %v, want %d 57014 ./semantic/zz_slow.sql deploy",
			env["exitCode"], env["sqlstate"], env["failedFile"], env["phase"], wantExit)
	}
	if out := pgmi.FormatError(err); !strings.Contains(out, "LOCATION: ./semantic/zz_slow.sql") {
		t.Errorf("text output does not name the file:\n%s", out)
	}
}

func TestDeployCmd_TimeoutNamesTheRunningFile(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const testDB = "pgmi_cli_timeout_located"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	env, err := deploySlowJSON(t, connString, testDB, 2*time.Second, nil)
	requireStoppedInSlowFile(t, env, err, pgmi.ExitTimeout)
	if msg, _ := env["error"].(string); !strings.Contains(msg, "--timeout 2s") {
		t.Errorf("error %q lost the --timeout hint", msg)
	}
}

func TestDeployCmd_InterruptNamesTheRunningFile(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const testDB = "pgmi_cli_interrupt_located"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	pool := testhelpers.GetTestPool(t, connString, "postgres")
	sleeping := func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var n int
			err := pool.QueryRow(context.Background(),
				"SELECT count(*) FROM pg_stat_activity WHERE datname = $1 AND wait_event = 'PgSleep'",
				testDB).Scan(&n)
			if err == nil && n > 0 {
				_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	env, err := deploySlowJSON(t, connString, testDB, time.Minute, sleeping)
	requireStoppedInSlowFile(t, env, err, pgmi.ExitInterrupted)
}
