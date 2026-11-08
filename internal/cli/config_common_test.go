package cli

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/db"
	"github.com/vvka-141/pgmi/internal/scaffold"
	"github.com/vvka-141/pgmi/pkg/pgmi"
	"gopkg.in/yaml.v3"
)

func TestLoadProjectConfig_EnvIsProjectScoped(t *testing.T) {
	const (
		keyTarget = "PGMI_TEST_ENV_TARGET"
		keyCwd    = "PGMI_TEST_ENV_CWD_ONLY"
	)

	tests := []struct {
		name        string
		projectEnv  string // contents of <project>/.env, "" means no file
		cwdEnv      string // contents of <cwd>/.env, "" means no file
		wantTarget  string // expected os.Getenv(keyTarget)
		wantCwdOnly string // expected os.Getenv(keyCwd)
	}{
		{
			name:        "loads project .env, not cwd .env",
			projectEnv:  keyTarget + "=from_project\n",
			cwdEnv:      keyTarget + "=from_cwd\n" + keyCwd + "=leaked\n",
			wantTarget:  "from_project",
			wantCwdOnly: "",
		},
		{
			name:        "missing project .env does not fall back to cwd",
			projectEnv:  "",
			cwdEnv:      keyTarget + "=from_cwd\n" + keyCwd + "=leaked\n",
			wantTarget:  "",
			wantCwdOnly: "",
		},
		{
			name:        "loads project .env when no cwd .env exists",
			projectEnv:  keyTarget + "=from_project\n",
			cwdEnv:      "",
			wantTarget:  "from_project",
			wantCwdOnly: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := t.TempDir()
			cwdDir := t.TempDir()
			if tt.projectEnv != "" {
				writeEnvFile(t, projectDir, tt.projectEnv)
			}
			if tt.cwdEnv != "" {
				writeEnvFile(t, cwdDir, tt.cwdEnv)
			}

			t.Chdir(cwdDir)
			// godotenv never overrides an already-set var, so start clean.
			unsetEnv(t, keyTarget)
			unsetEnv(t, keyCwd)

			if _, err := loadProjectConfig(projectDir); err != nil {
				t.Fatalf("loadProjectConfig() error = %v", err)
			}

			if got := os.Getenv(keyTarget); got != tt.wantTarget {
				t.Errorf("%s = %q, want %q", keyTarget, got, tt.wantTarget)
			}
			if got := os.Getenv(keyCwd); got != tt.wantCwdOnly {
				t.Errorf("%s = %q, want %q (cwd .env must not leak in)", keyCwd, got, tt.wantCwdOnly)
			}
		})
	}
}

func writeEnvFile(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(contents), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestAuthMethodToString(t *testing.T) {
	tests := []struct {
		method pgmi.AuthMethod
		want   string
	}{
		{pgmi.AuthMethodStandard, ""},
		{pgmi.AuthMethodAzureEntraID, "azure"},
		{pgmi.AuthMethodAWSIAM, "aws"},
		{pgmi.AuthMethodGoogleIAM, "google"},
	}

	for _, tt := range tests {
		got := authMethodToString(tt.method)
		if got != tt.want {
			t.Errorf("authMethodToString(%v) = %q, want %q", tt.method, got, tt.want)
		}
	}
}

func TestSaveConnectionToConfig_CloudAuth(t *testing.T) {
	dir := t.TempDir()

	connConfig := &db.ConnectionConfig{
		Host:           "myhost.postgres.database.azure.com",
		Port:           5432,
		Username:       "admin@myhost",
		Database:       "mydb",
		SSLMode:        "require",
		SSLCert:        "/path/client.crt",
		SSLKey:         "/path/client.key",
		SSLRootCert:    "/path/ca.crt",
		AuthMethod:     pgmi.AuthMethodAzureEntraID,
		AzureTenantID:  "my-tenant",
		AzureClientID:  "my-client",
		GoogleInstance: "",
		AWSRegion:      "",
	}

	err := saveConnectionToConfig(dir, connConfig, "postgres")
	if err != nil {
		t.Fatalf("saveConnectionToConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if cfg.Connection.AuthMethod != "azure" {
		t.Errorf("AuthMethod = %q, want %q", cfg.Connection.AuthMethod, "azure")
	}
	if cfg.Connection.AzureTenantID != "my-tenant" {
		t.Errorf("AzureTenantID = %q, want %q", cfg.Connection.AzureTenantID, "my-tenant")
	}
	if cfg.Connection.AzureClientID != "my-client" {
		t.Errorf("AzureClientID = %q, want %q", cfg.Connection.AzureClientID, "my-client")
	}
	if cfg.Connection.SSLCert != "/path/client.crt" {
		t.Errorf("SSLCert = %q, want %q", cfg.Connection.SSLCert, "/path/client.crt")
	}
	if cfg.Connection.SSLKey != "/path/client.key" {
		t.Errorf("SSLKey = %q, want %q", cfg.Connection.SSLKey, "/path/client.key")
	}
	if cfg.Connection.SSLRootCert != "/path/ca.crt" {
		t.Errorf("SSLRootCert = %q, want %q", cfg.Connection.SSLRootCert, "/path/ca.crt")
	}
}

func TestSaveConnectionToConfig_AWSAuth(t *testing.T) {
	dir := t.TempDir()

	connConfig := &db.ConnectionConfig{
		Host:       "myhost.rds.amazonaws.com",
		Port:       5432,
		Username:   "admin",
		Database:   "mydb",
		SSLMode:    "require",
		AuthMethod: pgmi.AuthMethodAWSIAM,
		AWSRegion:  "us-east-1",
	}

	err := saveConnectionToConfig(dir, connConfig, "postgres")
	if err != nil {
		t.Fatalf("saveConnectionToConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if cfg.Connection.AuthMethod != "aws" {
		t.Errorf("AuthMethod = %q, want %q", cfg.Connection.AuthMethod, "aws")
	}
	if cfg.Connection.AWSRegion != "us-east-1" {
		t.Errorf("AWSRegion = %q, want %q", cfg.Connection.AWSRegion, "us-east-1")
	}
}

func TestSaveConnectionToConfig_GoogleAuth(t *testing.T) {
	dir := t.TempDir()

	connConfig := &db.ConnectionConfig{
		Host:           "10.0.0.1",
		Port:           5432,
		Username:       "admin",
		Database:       "mydb",
		SSLMode:        "require",
		AuthMethod:     pgmi.AuthMethodGoogleIAM,
		GoogleInstance: "proj:region:inst",
	}

	err := saveConnectionToConfig(dir, connConfig, "postgres")
	if err != nil {
		t.Fatalf("saveConnectionToConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if cfg.Connection.AuthMethod != "google" {
		t.Errorf("AuthMethod = %q, want %q", cfg.Connection.AuthMethod, "google")
	}
	if cfg.Connection.GoogleInstance != "proj:region:inst" {
		t.Errorf("GoogleInstance = %q, want %q", cfg.Connection.GoogleInstance, "proj:region:inst")
	}
}

func TestSaveConnectionToConfig_StandardAuth_OmitsCloudFields(t *testing.T) {
	dir := t.TempDir()

	connConfig := &db.ConnectionConfig{
		Host:       "localhost",
		Port:       5432,
		Username:   "postgres",
		Database:   "mydb",
		SSLMode:    "prefer",
		AuthMethod: pgmi.AuthMethodStandard,
	}

	err := saveConnectionToConfig(dir, connConfig, "postgres")
	if err != nil {
		t.Fatalf("saveConnectionToConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var cfg config.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if cfg.Connection.AuthMethod != "" {
		t.Errorf("AuthMethod should be empty for standard auth, got %q", cfg.Connection.AuthMethod)
	}
	if cfg.Connection.AzureTenantID != "" {
		t.Errorf("AzureTenantID should be empty, got %q", cfg.Connection.AzureTenantID)
	}
}

func TestVerboseOutputNeverContainsSecrets(t *testing.T) {
	sentinels := []string{"SENTINEL_PASSWORD", "SENTINEL_CLIENT_SECRET"}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	logConnectionVerbose(&db.ConnectionConfig{
		Host:              "testhost",
		Port:              5432,
		Username:          "testuser",
		Database:          "testdb",
		Password:          sentinels[0],
		SSLMode:           "require",
		AuthMethod:        pgmi.AuthMethodAzureEntraID,
		AzureClientSecret: sentinels[1],
	}, "postgres", true)

	w.Close()
	os.Stderr = oldStderr

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	r.Close()
	output := string(buf[:n])

	if !strings.Contains(output, "testhost") {
		t.Fatal("verbose output should contain the host")
	}
	for _, s := range sentinels {
		if strings.Contains(output, s) {
			t.Errorf("verbose output must not contain secret %q, got:\n%s", s, output)
		}
	}
}

func TestVerboseParamLoadingNeverContainsValues(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "params.env")
	if err := os.WriteFile(envFile, []byte("api_key=SENTINEL_APIKEY\nother=SENTINEL_OTHER\n"), 0600); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	_, err := loadMergedParameters(nil, paramSources{files: []string{envFile}}, true)
	w.Close()
	os.Stderr = oldStderr

	var buf [8192]byte
	n, _ := r.Read(buf[:])
	r.Close()
	output := string(buf[:n])

	if err != nil {
		t.Fatalf("loadMergedParameters() error: %v", err)
	}
	// Positive control: the capture works and verbose output was produced, so
	// the absence checks below are not trivially satisfied by an empty buffer.
	if !strings.Contains(output, "Loaded 2 parameter(s)") {
		t.Fatalf("expected verbose parameter output, got:\n%s", output)
	}
	for _, s := range []string{"SENTINEL_APIKEY", "SENTINEL_OTHER"} {
		if strings.Contains(output, s) {
			t.Errorf("verbose param output must not contain value %q, got:\n%s", s, output)
		}
	}
}

// The session stores keys lowercased, so keys differing only in case across
// sources are one parameter: the later source wins instead of a 23505 on the
// session table. Within --param they are refused, as within one source.
func TestLoadMergedParameters_CaseInsensitivePrecedence(t *testing.T) {
	cfg := &config.ProjectConfig{Params: map[string]string{"DB_Url": "yaml"}}
	got, err := loadMergedParameters(cfg, paramSources{pairs: []string{"db_url=cli", "env=b"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"db_url": "cli", "env": "b"}
	if !maps.Equal(got, want) {
		t.Errorf("merged = %v, want %v", got, want)
	}
}

func TestLoadMergedParameters_CaseVariantsInYamlAreInvalidConfig(t *testing.T) {
	cfg := &config.ProjectConfig{Params: map[string]string{"Region": "eu", "region": "us"}}
	_, err := loadMergedParameters(cfg, paramSources{}, false)
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig (exit 10), got %v", err)
	}
	if !strings.Contains(err.Error(), "pgmi.yaml") {
		t.Errorf("error should name pgmi.yaml: %v", err)
	}
}

func TestValidateSSLMode(t *testing.T) {
	for _, mode := range []string{"", "disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		if err := validateSSLMode(mode); err != nil {
			t.Errorf("validateSSLMode(%q) = %v, want nil", mode, err)
		}
	}

	err := validateSSLMode("bogus")
	if err == nil {
		t.Fatal("validateSSLMode(\"bogus\") = nil, want error")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("error should wrap ErrInvalidConfig, got %v", err)
	}
}

func writeYAML(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "pgmi.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readYAML(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// commentsOutsideConnection returns every comment line that does not sit
// inside the indented body of the top-level connection: block.
func commentsOutsideConnection(content string) []string {
	var out []string
	inConnection := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "connection:"):
			inConnection = true
			continue
		case line != "" && !strings.HasPrefix(line, " "):
			inConnection = false
		}
		if inConnection {
			continue
		}
		if i := strings.Index(line, "#"); i >= 0 {
			out = append(out, strings.TrimSpace(line[i:]))
		} else if strings.HasPrefix(trimmed, "#") {
			out = append(out, trimmed)
		}
	}
	return out
}

func TestSaveConnectionToConfig_KeepsCommentsAndOtherKeys(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, `# my team's notes: never deploy prod from a laptop
connection:
  host: 127.0.0.1
  port: 55602
  database: trialw1
params:
  exchange_retention_days: "14"   # legal asked for 14
timeout: 10m
`)
	conn := &db.ConnectionConfig{Host: "db.internal", Port: 5433, Username: "app", Database: "trialw2", SSLMode: "prefer"}
	if err := saveConnectionToConfig(dir, conn, "postgres"); err != nil {
		t.Fatal(err)
	}
	got := readYAML(t, dir)

	for _, want := range []string{"# my team's notes: never deploy prod from a laptop", "# legal asked for 14"} {
		if !strings.Contains(got, want) {
			t.Errorf("comment %q lost:\n%s", want, got)
		}
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Params["exchange_retention_days"] != "14" || cfg.Timeout != "10m" {
		t.Errorf("params or timeout changed: %+v", cfg)
	}
	want := config.ConnectionConfig{Host: "db.internal", Port: 5433, Username: "app", Database: "trialw2", MaintenanceDatabase: "postgres", SSLMode: "prefer"}
	if cfg.Connection != want {
		t.Errorf("connection = %+v, want %+v", cfg.Connection, want)
	}
}

func TestSaveConnectionToConfig_KeepsTemplateGuidance(t *testing.T) {
	for _, template := range []string{"basic", "advanced"} {
		t.Run(template, func(t *testing.T) {
			original, err := scaffold.GetTemplatesFS().ReadFile("templates/" + template + "/pgmi.yaml")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			writeYAML(t, dir, string(original))

			conn := &db.ConnectionConfig{Host: "127.0.0.1", Port: 55601, Username: "postgres", Database: "appdb", SSLMode: "prefer"}
			if err := saveConnectionToConfig(dir, conn, "postgres"); err != nil {
				t.Fatal(err)
			}
			got := readYAML(t, dir)

			wantComments := commentsOutsideConnection(string(original))
			if len(wantComments) == 0 {
				t.Fatal("the template has no comments outside connection:, so this proves nothing")
			}
			for _, c := range wantComments {
				if !strings.Contains(got, c) {
					t.Errorf("comment %q lost:\n%s", c, got)
				}
			}
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatalf("saved file does not load: %v\n%s", err, got)
			}
			if cfg.Connection.Database != "appdb" || cfg.Connection.Port != 55601 {
				t.Errorf("connection not saved: %+v", cfg.Connection)
			}
			if strings.Contains(got, "change this!") {
				t.Errorf("the line comment of a changed value should go:\n%s", got)
			}
		})
	}
}

func TestSaveConnectionToConfig_SwitchingAuthDropsStaleKeys(t *testing.T) {
	dir := t.TempDir()
	aws := &db.ConnectionConfig{Host: "x.rds.amazonaws.com", Port: 5432, Username: "iam", Database: "app", SSLMode: "require",
		AuthMethod: pgmi.AuthMethodAWSIAM, AWSRegion: "eu-west-1"}
	if err := saveConnectionToConfig(dir, aws, "postgres"); err != nil {
		t.Fatal(err)
	}
	pw := &db.ConnectionConfig{Host: "localhost", Port: 5432, Username: "postgres", Database: "app", SSLMode: "prefer", AuthMethod: pgmi.AuthMethodStandard}
	if err := saveConnectionToConfig(dir, pw, "postgres"); err != nil {
		t.Fatal(err)
	}
	got := readYAML(t, dir)
	for _, stale := range []string{"aws_region", "auth_method"} {
		if strings.Contains(got, stale) {
			t.Errorf("%s survived the switch to password auth:\n%s", stale, got)
		}
	}
}

func loadProjectConfig(sourcePath string) (*config.ProjectConfig, error) {
	loadDotenv(sourcePath)
	return loadProjectYAML(sourcePath)
}

func TestSaveConnectionToConfig_ReplacesTheCommentedPlaceholder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p")
	if err := scaffold.NewScaffolder(false).CreateProject("p", "basic", dir); err != nil {
		t.Fatal(err)
	}
	conn := &db.ConnectionConfig{Host: "localhost", Port: 5432, Username: "postgres", Database: "app", SSLMode: "prefer"}
	if err := saveConnectionToConfig(dir, conn, "postgres"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pgmi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "username:"); n != 1 {
		t.Errorf("pgmi.yaml has %d username: lines, want 1:\n%s", n, data)
	}
	if !strings.Contains(string(data), "timeout: 3m") || !strings.Contains(string(data), "# Parameters passed to deploy.sql") {
		t.Errorf("comments and keys outside the placeholder must survive:\n%s", data)
	}
}
