package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/testhelpers"
)

// Secrets reach the session without a file on disk or a value in argv:
// from PGMI_PARAM_* and from --params-file - (stdin), with --param still on top.
func TestDeploy_ParamsFromEnvironmentAndStdin(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)

	const testDB = "pgmi_param_sources"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	const deploySQL = `DO $$
BEGIN
    IF current_setting('pgmi.from_env', true) IS DISTINCT FROM 'env'
       OR current_setting('pgmi.from_stdin', true) IS DISTINCT FROM 'stdin'
       OR current_setting('pgmi.shared', true) IS DISTINCT FROM 'stdin'
       OR current_setting('pgmi.top', true) IS DISTINCT FROM 'flag' THEN
        RAISE EXCEPTION 'parameters: env=% stdin=% shared=% top=%',
            current_setting('pgmi.from_env', true), current_setting('pgmi.from_stdin', true),
            current_setting('pgmi.shared', true), current_setting('pgmi.top', true);
    END IF;
END $$;
`
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "deploy.sql"), []byte(deploySQL), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := pgmiCommand(t, "deploy", projectDir,
		"--connection", connString, "-d", testDB, "--overwrite", "--force",
		"--params-file", "-", "--param", "top=flag")
	cmd.Env = append(cmd.Env, "PGMI_PARAM_FROM_ENV=env", "PGMI_PARAM_SHARED=env", "PGMI_PARAM_TOP=env")
	cmd.Stdin = strings.NewReader("from_stdin=stdin\nshared=stdin\ntop=stdin\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deploy failed: %v\n%s", err, out)
	}
}

func TestDeploy_EmptyEnvParamIsExit10(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "deploy.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := pgmiCommand(t, "deploy", projectDir,
		"--connection", "postgresql://u@127.0.0.1:1/postgres", "-d", "app", "--force")
	cmd.Env = append(cmd.Env, "PGMI_PARAM_DB_PASSWORD=")
	out, err := cmd.CombinedOutput()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 10 {
		t.Fatalf("want exit 10, got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PGMI_PARAM_DB_PASSWORD") {
		t.Errorf("output does not name the variable:\n%s", out)
	}
}
