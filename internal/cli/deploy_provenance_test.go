package cli

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var provenanceKeys = []string{
	"schemaVersion", "pgmiVersion", "contractVersion", "serverVersion",
	"target", "startedAt", "finishedAt",
}

// requireProvenance checks the keys a CI job keeps as the record of a run. A
// failed deploy rolls back, so for a failure this envelope is the only record.
func requireProvenance(t *testing.T, env map[string]any) {
	t.Helper()
	for _, k := range provenanceKeys {
		if _, ok := env[k]; !ok {
			t.Errorf("envelope has no %q key: %#v", k, env)
		}
	}
	wantVersion, _, _ := resolveVersionInfo()
	if env["pgmiVersion"] != wantVersion {
		t.Errorf("pgmiVersion = %#v, want %q", env["pgmiVersion"], wantVersion)
	}
	if env["schemaVersion"] != float64(deployJSONSchemaVersion) {
		t.Errorf("schemaVersion = %#v, want %d", env["schemaVersion"], deployJSONSchemaVersion)
	}
	for _, k := range []string{"startedAt", "finishedAt"} {
		s, _ := env[k].(string)
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			t.Errorf("%s = %#v is not an RFC 3339 timestamp", k, env[k])
		}
	}
}

func TestDeployJSON_ProvenanceOnSuccessAndFailure(t *testing.T) {
	prov := deployProvenance{
		startedAt: time.Now(),
		target:    &deployTarget{Host: "db.internal", Port: 6432, Database: "myapp"},
	}
	withServer := *sampleResult
	withServer.ServerVersion = "18.1"

	for name, deployErr := range map[string]error{"success": nil, "failure": errors.New("execution failed: boom")} {
		t.Run(name, func(t *testing.T) {
			env := decodeEnvelope(t, captureStdout(t, func() { printDeployJSON(&withServer, deployErr, prov) }))
			requireProvenance(t, env)
			if env["contractVersion"] != "1" || env["serverVersion"] != "18.1" {
				t.Errorf("contractVersion/serverVersion = %#v/%#v", env["contractVersion"], env["serverVersion"])
			}
			target, _ := env["target"].(map[string]any)
			if target["host"] != "db.internal" || target["port"] != float64(6432) || target["database"] != "myapp" {
				t.Errorf("target = %#v", env["target"])
			}
		})
	}
}

// The envelope is CI output, kept as an artifact and pasted into tickets. A
// credential from the connection string must not reach it: not the password,
// not an IAM token passed as one (an RDS token is a presigned URL).
func TestDeployCmd_JSONFailureCarriesProvenanceWithoutCredentials(t *testing.T) {
	const iamToken = "db.example.com:5432/?Action=connect&DBUser=app&X-Amz-Credential=AKIAEXAMPLE7Q%2F20261031&X-Amz-Signature=5f0e9c1d2b3a"
	for name, secret := range map[string]string{
		"password":  "pw-Zq81-not-real",
		"iam token": iamToken,
	} {
		t.Run(name, func(t *testing.T) {
			resetDeployFlags()
			clearPGEnv(t)
			deployFlags.connection = "postgresql://app:" + url.QueryEscape(secret) + "@127.0.0.1:1/postgres?connect_timeout=2"
			deployFlags.database = "app"
			deployFlags.force = true
			deployFlags.jsonOutput = true
			deployFlags.timeout = 10 * time.Second

			var err error
			out := captureStdout(t, func() { err = runDeploy(deployCmd, []string{deployProjectDir(t)}) })
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConnectionError {
				t.Fatalf("exit code %d, want %d: %v", got, pgmi.ExitConnectionError, err)
			}
			requireNoCredential(t, out, secret)

			env := decodeEnvelope(t, out)
			requireProvenance(t, env)
			if env["status"] != "failed" || env["serverVersion"] != nil {
				t.Errorf("status/serverVersion = %#v/%#v, want failed/null", env["status"], env["serverVersion"])
			}
			target, _ := env["target"].(map[string]any)
			if len(target) != 3 || target["host"] != "127.0.0.1" || target["port"] != float64(1) || target["database"] != "app" {
				t.Errorf("target = %#v, want exactly host, port and database", env["target"])
			}
		})
	}
}

func requireNoCredential(t *testing.T, out, secret string) {
	t.Helper()
	leaks := []string{secret, url.QueryEscape(secret)}
	if strings.Contains(secret, "X-Amz-Signature") {
		leaks = append(leaks, "X-Amz-Signature", "AKIAEXAMPLE7Q", "5f0e9c1d2b3a")
	}
	for _, leak := range leaks {
		if strings.Contains(out, leak) {
			t.Errorf("envelope leaked %q:\n%s", leak, out)
		}
	}
}
