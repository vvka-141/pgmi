package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetDeployFlags resets all deployment-related global flags to their zero values.
// This is necessary because flags are package-level globals that persist across tests.
func resetDeployFlags() {
	deployConnection = ""
	deployHost = ""
	deployPort = 0
	deployUsername = ""
	deployDatabase = ""
	deploySSLMode = ""
	deployOverwrite = false
	deployForce = false
	deployParams = nil
	deployParamsFiles = nil
	deployTimeout = 0
}

// TestBuildDeploymentConfig tests the deployment configuration building logic.
func TestBuildDeploymentConfig(t *testing.T) {
	// Save and restore original environment
	originalEnv := os.Getenv("PGMI_CONNECTION_STRING")
	defer func() {
		if originalEnv != "" {
			os.Setenv("PGMI_CONNECTION_STRING", originalEnv)
		} else {
			os.Unsetenv("PGMI_CONNECTION_STRING")
		}
	}()
	os.Unsetenv("PGMI_CONNECTION_STRING")

	// Create temporary directory for test files
	tempDir := t.TempDir()
	sourcePath := tempDir

	tests := []struct {
		name               string
		setupFlags         func()
		sourcePath         string
		verbose            bool
		wantDatabaseName   string
		wantMaintenanceDB  string
		wantOverwrite      bool
		wantForce          bool
		wantParamCount     int
		wantTimeout        time.Duration
		wantErr            bool
		wantErrContains    string
	}{
		{
			name: "basic deployment with database flag",
			setupFlags: func() {
				deployDatabase = "myapp"
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = false
				deployForce = false
				deployParams = nil
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "myapp",
			wantMaintenanceDB: "postgres",
			wantOverwrite:     false,
			wantForce:         false,
			wantParamCount:    0,
			wantTimeout:       3 * time.Minute,
			wantErr:           false,
		},
		{
			name: "deployment with overwrite and force flags",
			setupFlags: func() {
				deployDatabase = "testdb"
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = true
				deployForce = true
				deployParams = nil
				deployParamsFiles = nil
				deployTimeout = 5 * time.Minute
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "testdb",
			wantMaintenanceDB: "postgres",
			wantOverwrite:     true,
			wantForce:         true,
			wantParamCount:    0,
			wantTimeout:       5 * time.Minute,
			wantErr:           false,
		},
		{
			name: "deployment with CLI parameters",
			setupFlags: func() {
				deployDatabase = "myapp"
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = false
				deployForce = false
				deployParams = []string{"env=production", "region=us-west"}
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "myapp",
			wantMaintenanceDB: "postgres",
			wantOverwrite:     false,
			wantForce:         false,
			wantParamCount:    2,
			wantTimeout:       3 * time.Minute,
			wantErr:           false,
		},
		{
			name: "deployment with connection string",
			setupFlags: func() {
				deployConnection = "postgresql://user:pass@customhost:5433/mydb"
				deployDatabase = ""
				deployHost = ""
				deployPort = 0
				deployUsername = ""
				deployOverwrite = false
				deployForce = false
				deployParams = nil
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "mydb",
			wantMaintenanceDB: "postgres",
			wantOverwrite:     false,
			wantForce:         false,
			wantParamCount:    0,
			wantTimeout:       3 * time.Minute,
			wantErr:           false,
		},
		{
			name: "database flag overrides connection string database",
			setupFlags: func() {
				deployConnection = "postgresql://user:pass@customhost:5433/conndb"
				deployDatabase = "flagdb"
				deployHost = ""
				deployPort = 0
				deployUsername = ""
				deployOverwrite = false
				deployForce = false
				deployParams = nil
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "flagdb",
			wantMaintenanceDB: "conndb", // Maintenance DB comes from connection string when -d flag provided
			wantOverwrite:     false,
			wantForce:         false,
			wantParamCount:    0,
			wantTimeout:       3 * time.Minute,
			wantErr:           false,
		},
		{
			name: "error when no database provided",
			setupFlags: func() {
				deployConnection = ""
				deployDatabase = ""
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = false
				deployForce = false
				deployParams = nil
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "database name is required",
		},
		{
			name: "error with invalid CLI parameter format",
			setupFlags: func() {
				deployDatabase = "myapp"
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = false
				deployForce = false
				deployParams = []string{"invalid_param_without_equals"}
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "invalid parameter format",
		},
		{
			name: "custom timeout value",
			setupFlags: func() {
				deployDatabase = "myapp"
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = false
				deployForce = false
				deployParams = nil
				deployParamsFiles = nil
				deployTimeout = 10 * time.Minute
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "myapp",
			wantMaintenanceDB: "postgres",
			wantOverwrite:     false,
			wantForce:         false,
			wantParamCount:    0,
			wantTimeout:       10 * time.Minute,
			wantErr:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset all flags before each test
			resetDeployFlags()

			// Setup flags for this test case
			tt.setupFlags()

			// Build deployment config
			config, err := buildDeploymentConfig(tt.sourcePath, tt.verbose)

			// Check error expectations
			if (err != nil) != tt.wantErr {
				t.Errorf("buildDeploymentConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if tt.wantErrContains != "" && !contains(err.Error(), tt.wantErrContains) {
					t.Errorf("buildDeploymentConfig() error = %v, want error containing %q", err, tt.wantErrContains)
				}
				return
			}

			// Verify config values
			if config.DatabaseName != tt.wantDatabaseName {
				t.Errorf("buildDeploymentConfig() DatabaseName = %v, want %v", config.DatabaseName, tt.wantDatabaseName)
			}
			if config.MaintenanceDatabase != tt.wantMaintenanceDB {
				t.Errorf("buildDeploymentConfig() MaintenanceDatabase = %v, want %v", config.MaintenanceDatabase, tt.wantMaintenanceDB)
			}
			if config.Overwrite != tt.wantOverwrite {
				t.Errorf("buildDeploymentConfig() Overwrite = %v, want %v", config.Overwrite, tt.wantOverwrite)
			}
			if config.Force != tt.wantForce {
				t.Errorf("buildDeploymentConfig() Force = %v, want %v", config.Force, tt.wantForce)
			}
			if len(config.Parameters) != tt.wantParamCount {
				t.Errorf("buildDeploymentConfig() parameter count = %v, want %v", len(config.Parameters), tt.wantParamCount)
			}
			if config.Timeout != tt.wantTimeout {
				t.Errorf("buildDeploymentConfig() Timeout = %v, want %v", config.Timeout, tt.wantTimeout)
			}
			if config.Verbose != tt.verbose {
				t.Errorf("buildDeploymentConfig() Verbose = %v, want %v", config.Verbose, tt.verbose)
			}
			if config.SourcePath != tt.sourcePath {
				t.Errorf("buildDeploymentConfig() SourcePath = %v, want %v", config.SourcePath, tt.sourcePath)
			}
		})
	}
}

// TestBuildDeploymentConfig_ParameterPrecedence tests that CLI parameters override file parameters.
func TestBuildDeploymentConfig_ParameterPrecedence(t *testing.T) {
	// Reset flags
	resetDeployFlags()

	// Clear environment
	originalEnv := os.Getenv("PGMI_CONNECTION_STRING")
	defer func() {
		if originalEnv != "" {
			os.Setenv("PGMI_CONNECTION_STRING", originalEnv)
		} else {
			os.Unsetenv("PGMI_CONNECTION_STRING")
		}
	}()
	os.Unsetenv("PGMI_CONNECTION_STRING")

	// Create temp directory and params file
	tempDir := t.TempDir()
	paramsFile := filepath.Join(tempDir, "params.env")

	// Write params file with some values
	paramsContent := `env=staging
region=eu-west
api_key=file_secret
`
	if err := os.WriteFile(paramsFile, []byte(paramsContent), 0644); err != nil {
		t.Fatalf("Failed to create params file: %v", err)
	}

	// Setup flags
	deployDatabase = "myapp"
	deployHost = "localhost"
	deployPort = 5432
	deployUsername = "postgres"
	deployOverwrite = false
	deployForce = false
	deployParamsFiles = []string{paramsFile}
	deployParams = []string{"env=production", "version=1.2.3"} // Override env, add version
	deployTimeout = 3 * time.Minute

	// Build config
	config, err := buildDeploymentConfig(tempDir, false)
	if err != nil {
		t.Fatalf("buildDeploymentConfig() unexpected error: %v", err)
	}

	// Verify parameter precedence
	expectedParams := map[string]string{
		"env":     "production", // CLI overrides file
		"region":  "eu-west",    // From file
		"api_key": "file_secret", // From file
		"version": "1.2.3",      // CLI only
	}

	if len(config.Parameters) != len(expectedParams) {
		t.Errorf("parameter count = %v, want %v", len(config.Parameters), len(expectedParams))
	}

	for key, expectedValue := range expectedParams {
		if actualValue, ok := config.Parameters[key]; !ok {
			t.Errorf("missing parameter %q", key)
		} else if actualValue != expectedValue {
			t.Errorf("parameter %q = %v, want %v", key, actualValue, expectedValue)
		}
	}
}

// TestBuildDeploymentConfig_ValidationErrors tests various validation error scenarios.
func TestBuildDeploymentConfig_ValidationErrors(t *testing.T) {
	// Clear environment
	originalEnv := os.Getenv("PGMI_CONNECTION_STRING")
	defer func() {
		if originalEnv != "" {
			os.Setenv("PGMI_CONNECTION_STRING", originalEnv)
		} else {
			os.Unsetenv("PGMI_CONNECTION_STRING")
		}
	}()
	os.Unsetenv("PGMI_CONNECTION_STRING")

	tempDir := t.TempDir()

	tests := []struct {
		name            string
		setupFlags      func()
		wantErrContains string
	}{
		{
			name: "invalid connection string format",
			setupFlags: func() {
				deployConnection = "invalid://bad:format:here"
				deployDatabase = "myapp"
				deployHost = ""
				deployPort = 0
				deployUsername = ""
				deployOverwrite = false
				deployForce = false
				deployParams = nil
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			wantErrContains: "invalid connection string",
		},
		{
			name: "parameter missing equals sign",
			setupFlags: func() {
				deployDatabase = "myapp"
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = false
				deployForce = false
				deployParams = []string{"invalidparamwithoutequals"}
				deployParamsFiles = nil
				deployTimeout = 3 * time.Minute
			},
			wantErrContains: "invalid parameter format",
		},
		{
			name: "nonexistent params file",
			setupFlags: func() {
				deployDatabase = "myapp"
				deployHost = "localhost"
				deployPort = 5432
				deployUsername = "postgres"
				deployOverwrite = false
				deployForce = false
				deployParams = nil
				deployParamsFiles = []string{"/nonexistent/params.env"}
				deployTimeout = 3 * time.Minute
			},
			wantErrContains: "failed to read params file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetDeployFlags()
			tt.setupFlags()

			_, err := buildDeploymentConfig(tempDir, false)

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error = %v, want error containing %q", err, tt.wantErrContains)
			}
		})
	}
}

// TestBuildDeploymentConfig_Validate tests that the returned config passes validation.
func TestBuildDeploymentConfig_Validate(t *testing.T) {
	// Clear environment
	originalEnv := os.Getenv("PGMI_CONNECTION_STRING")
	defer func() {
		if originalEnv != "" {
			os.Setenv("PGMI_CONNECTION_STRING", originalEnv)
		} else {
			os.Unsetenv("PGMI_CONNECTION_STRING")
		}
	}()
	os.Unsetenv("PGMI_CONNECTION_STRING")

	tempDir := t.TempDir()

	// Reset and setup valid flags
	resetDeployFlags()
	deployDatabase = "myapp"
	deployHost = "localhost"
	deployPort = 5432
	deployUsername = "postgres"
	deployOverwrite = false
	deployForce = false
	deployParams = []string{"env=test"}
	deployParamsFiles = nil
	deployTimeout = 3 * time.Minute

	config, err := buildDeploymentConfig(tempDir, false)
	if err != nil {
		t.Fatalf("buildDeploymentConfig() unexpected error: %v", err)
	}

	// Verify the config passes validation
	if err := config.Validate(); err != nil {
		t.Errorf("config.Validate() failed: %v", err)
	}

	// Verify required fields are populated
	if config.SourcePath == "" {
		t.Error("config.SourcePath is empty")
	}
	if config.DatabaseName == "" {
		t.Error("config.DatabaseName is empty")
	}
	if config.ConnectionString == "" {
		t.Error("config.ConnectionString is empty")
	}
}
