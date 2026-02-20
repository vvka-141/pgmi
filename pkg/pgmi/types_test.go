package pgmi_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestDeploymentConfig_Validate(t *testing.T) {
	tests := []struct {
		name      string
		config    pgmi.DeploymentConfig
		wantError bool
		errorType error
	}{
		{
			name: "valid config",
			config: pgmi.DeploymentConfig{
				SourcePath:       "./migrations",
				DatabaseName:     "mydb",
				ConnectionString: "postgresql://localhost:5432/postgres",
				Overwrite:        false,
				Force:            false,
			},
			wantError: false,
		},
		{
			name: "valid config with overwrite and force",
			config: pgmi.DeploymentConfig{
				SourcePath:       "./migrations",
				DatabaseName:     "mydb",
				ConnectionString: "postgresql://localhost:5432/postgres",
				Overwrite:        true,
				Force:            true,
			},
			wantError: false,
		},
		{
			name: "missing source path",
			config: pgmi.DeploymentConfig{
				DatabaseName:     "mydb",
				ConnectionString: "postgresql://localhost:5432/postgres",
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "missing database name",
			config: pgmi.DeploymentConfig{
				SourcePath:       "./migrations",
				ConnectionString: "postgresql://localhost:5432/postgres",
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "missing connection string",
			config: pgmi.DeploymentConfig{
				SourcePath:   "./migrations",
				DatabaseName: "mydb",
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "force without overwrite",
			config: pgmi.DeploymentConfig{
				SourcePath:       "./migrations",
				DatabaseName:     "mydb",
				ConnectionString: "postgresql://localhost:5432/postgres",
				Force:            true,
				Overwrite:        false,
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "negative timeout",
			config: pgmi.DeploymentConfig{
				SourcePath:       "./migrations",
				DatabaseName:     "mydb",
				ConnectionString: "postgresql://localhost:5432/postgres",
				Timeout:          -1 * time.Second,
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "multiple validation errors",
			config: pgmi.DeploymentConfig{
				Force:   true,
				Timeout: -1 * time.Second,
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantError {
				if err == nil {
					t.Errorf("Validate() expected error, got nil")
					return
				}

				if tt.errorType != nil && !errors.Is(err, tt.errorType) {
					t.Errorf("Validate() error type = %v, want %v", err, tt.errorType)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestConnectionConfig_DeepCopy(t *testing.T) {
	t.Run("copies AdditionalParams independently", func(t *testing.T) {
		orig := pgmi.ConnectionConfig{
			Host:             "localhost",
			Port:             5432,
			AdditionalParams: map[string]string{"a": "1", "b": "2"},
		}
		cp := orig.DeepCopy()

		cp.AdditionalParams["a"] = "changed"
		cp.Host = "remote"

		if orig.AdditionalParams["a"] != "1" {
			t.Error("DeepCopy did not isolate AdditionalParams map")
		}
		if orig.Host == "remote" {
			t.Error("DeepCopy did not isolate scalar fields")
		}
		if len(cp.AdditionalParams) != 2 {
			t.Errorf("expected 2 params in copy, got %d", len(cp.AdditionalParams))
		}
	})

	t.Run("nil AdditionalParams stays nil", func(t *testing.T) {
		orig := pgmi.ConnectionConfig{Host: "localhost"}
		cp := orig.DeepCopy()

		if cp.AdditionalParams != nil {
			t.Error("expected nil AdditionalParams in copy")
		}
	})

	t.Run("empty AdditionalParams stays empty", func(t *testing.T) {
		orig := pgmi.ConnectionConfig{
			AdditionalParams: map[string]string{},
		}
		cp := orig.DeepCopy()

		if cp.AdditionalParams == nil {
			t.Error("expected non-nil empty map in copy")
		}
		if len(cp.AdditionalParams) != 0 {
			t.Errorf("expected empty map in copy, got %d entries", len(cp.AdditionalParams))
		}
	})
}

func TestAuthMethod_String(t *testing.T) {
	tests := []struct {
		method pgmi.AuthMethod
		want   string
	}{
		{pgmi.AuthMethodStandard, "Standard"},
		{pgmi.AuthMethodAWSIAM, "AWS IAM"},
		{pgmi.AuthMethodGoogleIAM, "Google IAM"},
		{pgmi.AuthMethodAzureEntraID, "Azure Entra ID"},
		{pgmi.AuthMethod(99), "Unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.method.String(); got != tt.want {
				t.Errorf("AuthMethod(%d).String() = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestTestConfig_Validate(t *testing.T) {
	tests := []struct {
		name      string
		config    pgmi.TestConfig
		wantError bool
		errorType error
	}{
		{
			name: "valid config with all fields",
			config: pgmi.TestConfig{
				SourcePath:       "./myapp",
				DatabaseName:     "test_db",
				ConnectionString: "postgresql://localhost:5432/postgres",
				FilterPattern:    ".*",
				Parameters:       map[string]string{"key": "value"},
				Verbose:          true,
			},
			wantError: false,
		},
		{
			name: "valid config with minimal fields",
			config: pgmi.TestConfig{
				SourcePath:       "./myapp",
				DatabaseName:     "test_db",
				ConnectionString: "postgresql://localhost:5432/postgres",
			},
			wantError: false,
		},
		{
			name: "valid config with custom filter pattern",
			config: pgmi.TestConfig{
				SourcePath:       "./myapp",
				DatabaseName:     "test_db",
				ConnectionString: "postgresql://localhost:5432/postgres",
				FilterPattern:    "/auth/.*",
			},
			wantError: false,
		},
		{
			name: "valid config with list only mode",
			config: pgmi.TestConfig{
				SourcePath:       "./myapp",
				DatabaseName:     "test_db",
				ConnectionString: "postgresql://localhost:5432/postgres",
				ListOnly:         true,
			},
			wantError: false,
		},
		{
			name: "missing source path",
			config: pgmi.TestConfig{
				DatabaseName:     "test_db",
				ConnectionString: "postgresql://localhost:5432/postgres",
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "missing database name",
			config: pgmi.TestConfig{
				SourcePath:       "./myapp",
				ConnectionString: "postgresql://localhost:5432/postgres",
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "missing connection string",
			config: pgmi.TestConfig{
				SourcePath:   "./myapp",
				DatabaseName: "test_db",
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "multiple validation errors",
			config: pgmi.TestConfig{
				FilterPattern: "/auth/",
			},
			wantError: true,
			errorType: pgmi.ErrInvalidConfig,
		},
		{
			name: "empty filter pattern defaults to match all",
			config: pgmi.TestConfig{
				SourcePath:       "./myapp",
				DatabaseName:     "test_db",
				ConnectionString: "postgresql://localhost:5432/postgres",
				FilterPattern:    "",
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantError {
				if err == nil {
					t.Errorf("Validate() expected error, got nil")
					return
				}

				if tt.errorType != nil && !errors.Is(err, tt.errorType) {
					t.Errorf("Validate() error type = %v, want %v", err, tt.errorType)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}
