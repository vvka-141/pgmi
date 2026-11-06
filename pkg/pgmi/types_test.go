package pgmi_test

import (
	"errors"
	"testing"

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
			name: "force without overwrite is valid",
			config: pgmi.DeploymentConfig{
				SourcePath:       "./migrations",
				DatabaseName:     "mydb",
				ConnectionString: "postgresql://localhost:5432/postgres",
				Force:            true,
				Overwrite:        false,
			},
			wantError: false,
		},
		{
			name: "multiple validation errors",
			config: pgmi.DeploymentConfig{
				Force: true,
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
