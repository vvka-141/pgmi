package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
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

			if _, err := loadProjectConfig(projectDir, false); err != nil {
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

	connConfig := &pgmi.ConnectionConfig{
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

	connConfig := &pgmi.ConnectionConfig{
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

	connConfig := &pgmi.ConnectionConfig{
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

	connConfig := &pgmi.ConnectionConfig{
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
