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
