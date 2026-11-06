package cli

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

var provenanceKeys = []string{
	"schemaVersion", "pgmiVersion", "contractVersion", "serverVersion", "tls",
	"target", "connectionSource", "overrides", "dotenvKeys", "startedAt", "finishedAt",
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
	withServer.TLS = &pgmi.TLSInfo{Enabled: true, Version: "TLS 1.3", SSLMode: "verify-full"}

	for name, deployErr := range map[string]error{"success": nil, "failure": errors.New("execution failed: boom")} {
		t.Run(name, func(t *testing.T) {
			env := decodeEnvelope(t, captureStdout(t, func() { printDeployJSON(&withServer, deployErr, prov) }))
			requireProvenance(t, env)
			if env["contractVersion"] != "1" || env["serverVersion"] != "18.1" {
				t.Errorf("contractVersion/serverVersion = %#v/%#v", env["contractVersion"], env["serverVersion"])
			}
			tls, _ := env["tls"].(map[string]any)
			if tls["enabled"] != true || tls["version"] != "TLS 1.3" || tls["sslmode"] != "verify-full" {
				t.Errorf("tls = %#v", env["tls"])
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

// CI is told to deploy with --json, so the override notes must reach both
// stderr and the envelope there, and stdout must stay one JSON document.
func TestDeployCmd_JSONNamesConnectionSourceAndOverrides(t *testing.T) {
	resetDeployFlags()
	clearPGEnv(t)
	dir := deployProjectDir(t)
	if err := os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte("connection:\n  database: myapp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "postgresql://app@127.0.0.1:1?connect_timeout=2")
	t.Setenv("PGDATABASE", "fromenv")
	deployFlags.force = true
	deployFlags.jsonOutput = true
	deployFlags.timeout = 10 * time.Second

	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() { _ = runDeploy(deployCmd, []string{dir}) })
	})

	const note = "database: from PGDATABASE (fromenv), not pgmi.yaml (myapp)"
	if !strings.Contains(stderr, note) || !strings.Contains(stderr, "connection: from DATABASE_URL") {
		t.Errorf("--json hid the connection lines from stderr:\n%s", stderr)
	}
	env := decodeEnvelope(t, out)
	requireProvenance(t, env)
	if env["connectionSource"] != "DATABASE_URL" {
		t.Errorf("connectionSource = %#v, want DATABASE_URL", env["connectionSource"])
	}
	overrides, _ := env["overrides"].([]any)
	if len(overrides) != 1 {
		t.Fatalf("overrides = %#v, want one entry for the database", env["overrides"])
	}
	o, _ := overrides[0].(map[string]any)
	want := map[string]any{"field": "database", "from": "PGDATABASE", "value": "fromenv", "notFrom": "pgmi.yaml", "notValue": "myapp"}
	for k, v := range want {
		if o[k] != v {
			t.Errorf("overrides[0][%q] = %#v, want %#v", k, o[k], v)
		}
	}
}

func TestDeployCmd_NamesTheConnectionSourceWithoutAConnectionString(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, port, source, line string
	}{
		{"pgmi.yaml", "connection:\n  host: 127.0.0.1\n  port: 1\n  database: myapp\n", "", "pgmi.yaml",
			"connection: from pgmi.yaml (127.0.0.1:1/myapp)"},
		{"defaults", "connection:\n  database: myapp\n", "1", "defaults", "connection: defaults (localhost:1/myapp)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetDeployFlags()
			clearPGEnv(t)
			dir := deployProjectDir(t)
			if err := os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.port != "" {
				t.Setenv("PGPORT", tc.port)
			}
			deployFlags.force = true
			deployFlags.jsonOutput = true
			deployFlags.timeout = 10 * time.Second

			var out string
			stderr := captureStderr(t, func() {
				out = captureStdout(t, func() { _ = runDeploy(deployCmd, []string{dir}) })
			})
			if !strings.Contains(stderr, tc.line) {
				t.Errorf("stderr lacks %q:\n%s", tc.line, stderr)
			}
			if env := decodeEnvelope(t, out); env["connectionSource"] != tc.source {
				t.Errorf("connectionSource = %#v, want %q", env["connectionSource"], tc.source)
			}
		})
	}
}

// A project .env can decide the target over the pipeline's own PG* variables:
// a connection string set only in .env outranks them. The deploy names the
// file and the keys it applied, never their values, on stderr and in --json.
func TestDeployCmd_NamesWhatDotenvApplied(t *testing.T) {
	const secret = "dotenv-Secret-77"
	resetDeployFlags()
	clearPGEnv(t)
	dir := deployProjectDir(t)
	env := "PGMI_CONNECTION_STRING=postgresql://app:" + secret + "@127.0.0.1:1/dotenv_db?connect_timeout=2\n" +
		"PGMI_PARAM_TOKEN=" + secret + "\n" +
		"PGDATABASE=from_dotenv\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGDATABASE", "from_ci")
	t.Cleanup(func() {
		for _, k := range []string{"PGMI_CONNECTION_STRING", "PGMI_PARAM_TOKEN"} {
			_ = os.Unsetenv(k)
		}
	})
	deployFlags.force = true
	deployFlags.jsonOutput = true
	deployFlags.timeout = 10 * time.Second

	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() { _ = runDeploy(deployCmd, []string{dir}) })
	})

	for _, want := range []string{
		"connection: from PGMI_CONNECTION_STRING (set by " + filepath.Join(dir, ".env") + ")",
		filepath.Join(dir, ".env") + " applied: PGMI_CONNECTION_STRING, PGMI_PARAM_TOKEN",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	for name, text := range map[string]string{"stderr": stderr, "envelope": out} {
		if strings.Contains(text, secret) {
			t.Errorf("the .env value appears in the %s:\n%s", name, text)
		}
	}

	envelope := decodeEnvelope(t, out)
	keys, _ := envelope["dotenvKeys"].([]any)
	if len(keys) != 2 || keys[0] != "PGMI_CONNECTION_STRING" || keys[1] != "PGMI_PARAM_TOKEN" {
		t.Errorf("dotenvKeys = %#v, want the two keys .env applied, not PGDATABASE, which CI set", envelope["dotenvKeys"])
	}
}
