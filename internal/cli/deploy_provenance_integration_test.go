package cli

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/testhelpers"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func provenanceProject(t *testing.T, deploySQL string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte(deploySQL), 0644); err != nil {
		t.Fatalf("writing deploy.sql: %v", err)
	}
	return dir
}

func deployJSON(t *testing.T, connString, database, dir string) (map[string]any, string, error) {
	t.Helper()
	resetDeployFlags()
	clearPGEnv(t)
	deployFlags.connection = connString
	deployFlags.database = database
	deployFlags.force = true
	deployFlags.jsonOutput = true
	deployFlags.timeout = 60 * time.Second

	var err error
	out := captureStdout(t, func() { err = runDeploy(deployCmd, []string{dir}) })
	return decodeEnvelope(t, out), out, err
}

// deploy.sql can refuse a binary it was not written for only if it can see
// which binary runs it: pgmi_runtime_view must carry what `pgmi version` says.
func TestDeployCmd_RuntimeViewCarriesBinaryVersion(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const testDB = "pgmi_cli_runtime_view"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	dir := provenanceProject(t, "BEGIN;\nCREATE TABLE public.runtime_snapshot AS SELECT * FROM pg_temp.pgmi_runtime_view;\nCOMMIT;\n")
	env, _, err := deployJSON(t, connString, testDB, dir)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	requireProvenance(t, env)

	var rows int
	var pgmiVersion, contractVersion, serverVersion string
	pool := testhelpers.GetTestPool(t, connString, testDB)
	ctx := context.Background()
	if err := pool.QueryRow(ctx, "SELECT count(*) OVER (), pgmi_version, contract_version FROM public.runtime_snapshot").
		Scan(&rows, &pgmiVersion, &contractVersion); err != nil {
		t.Fatalf("read runtime snapshot: %v", err)
	}
	if err := pool.QueryRow(ctx, "SHOW server_version").Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}

	wantVersion, _, _ := resolveVersionInfo()
	if rows != 1 || pgmiVersion != wantVersion || contractVersion != "1" {
		t.Errorf("pgmi_runtime_view = %d row(s) (%q, %q), want 1 row (%q, %q)",
			rows, pgmiVersion, contractVersion, wantVersion, "1")
	}
	if env["serverVersion"] != serverVersion || env["contractVersion"] != "1" {
		t.Errorf("envelope serverVersion/contractVersion = %#v/%#v, want %q/1",
			env["serverVersion"], env["contractVersion"], serverVersion)
	}
}

// A deploy that fails after connecting still names the server it ran on.
func TestDeployCmd_JSONSQLFailureKeepsServerVersion(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	const testDB = "pgmi_cli_json_sql_failure"
	defer testhelpers.CleanupTestDB(t, connString, testDB)

	env, _, err := deployJSON(t, connString, testDB, provenanceProject(t, "SELECT this_function_does_not_exist();\n"))
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitExecutionFailed {
		t.Fatalf("exit code %d, want %d: %v", got, pgmi.ExitExecutionFailed, err)
	}
	requireProvenance(t, env)
	if s, _ := env["serverVersion"].(string); s == "" {
		t.Errorf("serverVersion = %#v on a failure after connecting", env["serverVersion"])
	}
}

// The server rejects the credential; the envelope must still not repeat it.
func TestDeployCmd_JSONAuthFailureCarriesNoCredential(t *testing.T) {
	connString := testhelpers.RequireDatabase(t)
	u, err := url.Parse(connString)
	if err != nil {
		t.Skipf("PGMI_TEST_CONN is not a URI: %v", err)
	}

	for name, secret := range map[string]string{
		"password":  "pw-Zq81-not-real",
		"iam token": "db.example.com:5432/?Action=connect&DBUser=app&X-Amz-Credential=AKIAEXAMPLE7Q%2F20261031&X-Amz-Signature=5f0e9c1d2b3a",
	} {
		t.Run(name, func(t *testing.T) {
			bad := *u
			bad.User = url.UserPassword(u.User.Username(), secret)

			env, out, err := deployJSON(t, bad.String(), "pgmi_cli_json_auth_failure", provenanceProject(t, "SELECT 1;\n"))
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConnectionError {
				t.Fatalf("exit code %d, want %d: %v", got, pgmi.ExitConnectionError, err)
			}
			requireNoCredential(t, out, secret)
			requireProvenance(t, env)
			target, _ := env["target"].(map[string]any)
			if target["host"] != u.Hostname() || target["database"] != "pgmi_cli_json_auth_failure" {
				t.Errorf("target = %#v", env["target"])
			}
		})
	}
}
