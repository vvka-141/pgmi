package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
	"gopkg.in/yaml.v3"
)

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
