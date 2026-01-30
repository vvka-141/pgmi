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
	deployFlags = deployFlagValues{}
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
				deployFlags.database = "myapp"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = nil
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
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
				deployFlags.database = "testdb"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = true
				deployFlags.force = true
				deployFlags.params = nil
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 5 * time.Minute
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
				deployFlags.database = "myapp"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = []string{"env=production", "region=us-west"}
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
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
				deployFlags.connection = "postgresql://user:pass@customhost:5433/mydb"
				deployFlags.database = ""
				deployFlags.host = ""
				deployFlags.port = 0
				deployFlags.username = ""
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = nil
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
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
				deployFlags.connection = "postgresql://user:pass@customhost:5433/conndb"
				deployFlags.database = "flagdb"
				deployFlags.host = ""
				deployFlags.port = 0
				deployFlags.username = ""
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = nil
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
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
				deployFlags.connection = ""
				deployFlags.database = ""
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = nil
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "database name is required",
		},
		{
			name: "error with invalid CLI parameter format",
			setupFlags: func() {
				deployFlags.database = "myapp"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = []string{"invalid_param_without_equals"}
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "invalid parameter format",
		},
		{
			name: "custom timeout value",
			setupFlags: func() {
				deployFlags.database = "myapp"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = nil
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 10 * time.Minute
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
			config, err := buildDeploymentConfig(deployCmd, tt.sourcePath, tt.verbose)

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
	deployFlags.database = "myapp"
	deployFlags.host = "localhost"
	deployFlags.port = 5432
	deployFlags.username = "postgres"
	deployFlags.overwrite = false
	deployFlags.force = false
	deployFlags.paramsFiles = []string{paramsFile}
	deployFlags.params = []string{"env=production", "version=1.2.3"} // Override env, add version
	deployFlags.timeout = 3 * time.Minute

	// Build config
	config, err := buildDeploymentConfig(deployCmd, tempDir, false)
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
				deployFlags.connection = "invalid://bad:format:here"
				deployFlags.database = "myapp"
				deployFlags.host = ""
				deployFlags.port = 0
				deployFlags.username = ""
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = nil
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
			},
			wantErrContains: "invalid connection string",
		},
		{
			name: "parameter missing equals sign",
			setupFlags: func() {
				deployFlags.database = "myapp"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = []string{"invalidparamwithoutequals"}
				deployFlags.paramsFiles = nil
				deployFlags.timeout = 3 * time.Minute
			},
			wantErrContains: "invalid parameter format",
		},
		{
			name: "nonexistent params file",
			setupFlags: func() {
				deployFlags.database = "myapp"
				deployFlags.host = "localhost"
				deployFlags.port = 5432
				deployFlags.username = "postgres"
				deployFlags.overwrite = false
				deployFlags.force = false
				deployFlags.params = nil
				deployFlags.paramsFiles = []string{"/nonexistent/params.env"}
				deployFlags.timeout = 3 * time.Minute
			},
			wantErrContains: "failed to read params file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetDeployFlags()
			tt.setupFlags()

			_, err := buildDeploymentConfig(deployCmd, tempDir, false)

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
	deployFlags.database = "myapp"
	deployFlags.host = "localhost"
	deployFlags.port = 5432
	deployFlags.username = "postgres"
	deployFlags.overwrite = false
	deployFlags.force = false
	deployFlags.params = []string{"env=test"}
	deployFlags.paramsFiles = nil
	deployFlags.timeout = 3 * time.Minute

	config, err := buildDeploymentConfig(deployCmd, tempDir, false)
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
