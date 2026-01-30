package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// resetTestFlags resets all test command-related global flags to their zero values.
// This is necessary because flags are package-level globals that persist across tests.
func resetTestFlags() {
	testFlags = testFlagValues{}
}

// TestBuildTestConfig tests the test configuration building logic.
func TestBuildTestConfig(t *testing.T) {
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
		name             string
		setupFlags       func()
		sourcePath       string
		verbose          bool
		wantDatabaseName string
		wantFilterPattern string
		wantListOnly     bool
		wantParamCount   int
		wantErr          bool
		wantErrContains  string
	}{
		{
			name: "basic test config with database flag",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "test_db",
			wantFilterPattern: ".*",
			wantListOnly:      false,
			wantParamCount:    0,
			wantErr:           false,
		},
		{
			name: "test config with custom filter pattern",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = "/pre-deployment/"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "test_db",
			wantFilterPattern: "/pre-deployment/",
			wantListOnly:      false,
			wantParamCount:    0,
			wantErr:           false,
		},
		{
			name: "test config with list mode enabled",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = ".*"
				testFlags.list = true
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "test_db",
			wantFilterPattern: ".*",
			wantListOnly:      true,
			wantParamCount:    0,
			wantErr:           false,
		},
		{
			name: "test config with CLI parameters",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = []string{"test_mode=fast", "verbose_output=true"}
				testFlags.paramsFiles = nil
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "test_db",
			wantFilterPattern: ".*",
			wantListOnly:      false,
			wantParamCount:    2,
			wantErr:           false,
		},
		{
			name: "test config with connection string",
			setupFlags: func() {
				testFlags.connection = "postgresql://user:pass@testhost:5433/testdb"
				testFlags.database = ""
				testFlags.host = ""
				testFlags.port = 0
				testFlags.username = ""
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "testdb",
			wantFilterPattern: ".*",
			wantListOnly:      false,
			wantParamCount:    0,
			wantErr:           false,
		},
		{
			name: "database flag overrides connection string database",
			setupFlags: func() {
				testFlags.connection = "postgresql://user:pass@testhost:5433/conndb"
				testFlags.database = "override_db"
				testFlags.host = ""
				testFlags.port = 0
				testFlags.username = ""
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "override_db",
			wantFilterPattern: ".*",
			wantListOnly:      false,
			wantParamCount:    0,
			wantErr:           false,
		},
		{
			name: "error when no database provided",
			setupFlags: func() {
				testFlags.connection = ""
				testFlags.database = ""
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "database name is required",
		},
		{
			name: "error with invalid CLI parameter format",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = []string{"invalid_param_no_equals"}
				testFlags.paramsFiles = nil
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "invalid parameter format",
		},
		{
			name: "regex filter for auth tests",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = "^\\./pgitest/auth/"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			sourcePath:        sourcePath,
			verbose:           false,
			wantDatabaseName:  "test_db",
			wantFilterPattern: "^\\./pgitest/auth/",
			wantListOnly:      false,
			wantParamCount:    0,
			wantErr:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset all flags before each test
			resetTestFlags()

			// Setup flags for this test case
			tt.setupFlags()

			// Build test config
			config, err := buildTestConfig(testCmd, tt.sourcePath, tt.verbose)

			// Check error expectations
			if (err != nil) != tt.wantErr {
				t.Errorf("buildTestConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				if tt.wantErrContains != "" && !contains(err.Error(), tt.wantErrContains) {
					t.Errorf("buildTestConfig() error = %v, want error containing %q", err, tt.wantErrContains)
				}
				return
			}

			// Verify config values
			if config.DatabaseName != tt.wantDatabaseName {
				t.Errorf("buildTestConfig() DatabaseName = %v, want %v", config.DatabaseName, tt.wantDatabaseName)
			}
			if config.FilterPattern != tt.wantFilterPattern {
				t.Errorf("buildTestConfig() FilterPattern = %v, want %v", config.FilterPattern, tt.wantFilterPattern)
			}
			if config.ListOnly != tt.wantListOnly {
				t.Errorf("buildTestConfig() ListOnly = %v, want %v", config.ListOnly, tt.wantListOnly)
			}
			if len(config.Parameters) != tt.wantParamCount {
				t.Errorf("buildTestConfig() parameter count = %v, want %v", len(config.Parameters), tt.wantParamCount)
			}
			if config.Verbose != tt.verbose {
				t.Errorf("buildTestConfig() Verbose = %v, want %v", config.Verbose, tt.verbose)
			}
			if config.SourcePath != tt.sourcePath {
				t.Errorf("buildTestConfig() SourcePath = %v, want %v", config.SourcePath, tt.sourcePath)
			}
		})
	}
}

// TestBuildTestConfig_ParameterPrecedence tests that CLI parameters override file parameters.
func TestBuildTestConfig_ParameterPrecedence(t *testing.T) {
	// Reset flags
	resetTestFlags()

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
	paramsFile := filepath.Join(tempDir, "test_params.env")

	// Write params file with some values
	paramsContent := `test_mode=slow
test_env=staging
test_user_id=12345
`
	if err := os.WriteFile(paramsFile, []byte(paramsContent), 0644); err != nil {
		t.Fatalf("Failed to create params file: %v", err)
	}

	// Setup flags
	testFlags.database = "test_db"
	testFlags.host = "localhost"
	testFlags.port = 5432
	testFlags.username = "postgres"
	testFlags.filter = ".*"
	testFlags.list = false
	testFlags.paramsFiles = []string{paramsFile}
	testFlags.params = []string{"test_mode=fast", "test_isolation=true"} // Override test_mode, add test_isolation

	// Build config
	config, err := buildTestConfig(testCmd, tempDir, false)
	if err != nil {
		t.Fatalf("buildTestConfig() unexpected error: %v", err)
	}

	// Verify parameter precedence
	expectedParams := map[string]string{
		"test_mode":      "fast",   // CLI overrides file
		"test_env":       "staging", // From file
		"test_user_id":   "12345",  // From file
		"test_isolation": "true",   // CLI only
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

// TestBuildTestConfig_ValidationErrors tests various validation error scenarios.
func TestBuildTestConfig_ValidationErrors(t *testing.T) {
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
				testFlags.connection = "invalid://bad:format"
				testFlags.database = "test_db"
				testFlags.host = ""
				testFlags.port = 0
				testFlags.username = ""
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = nil
			},
			wantErrContains: "invalid connection string",
		},
		{
			name: "parameter missing equals sign",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = []string{"badparam"}
				testFlags.paramsFiles = nil
			},
			wantErrContains: "invalid parameter format",
		},
		{
			name: "nonexistent params file",
			setupFlags: func() {
				testFlags.database = "test_db"
				testFlags.host = "localhost"
				testFlags.port = 5432
				testFlags.username = "postgres"
				testFlags.filter = ".*"
				testFlags.list = false
				testFlags.params = nil
				testFlags.paramsFiles = []string{"/path/to/nonexistent/file.env"}
			},
			wantErrContains: "failed to read params file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTestFlags()
			tt.setupFlags()

			_, err := buildTestConfig(testCmd, tempDir, false)

			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !contains(err.Error(), tt.wantErrContains) {
				t.Errorf("error = %v, want error containing %q", err, tt.wantErrContains)
			}
		})
	}
}

// TestBuildTestConfig_Validate tests that the returned config passes validation.
func TestBuildTestConfig_Validate(t *testing.T) {
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
	resetTestFlags()
	testFlags.database = "test_db"
	testFlags.host = "localhost"
	testFlags.port = 5432
	testFlags.username = "postgres"
	testFlags.filter = ".*"
	testFlags.list = false
	testFlags.params = []string{"env=test"}
	testFlags.paramsFiles = nil

	config, err := buildTestConfig(testCmd, tempDir, false)
	if err != nil {
		t.Fatalf("buildTestConfig() unexpected error: %v", err)
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
	if config.FilterPattern == "" {
		t.Error("config.FilterPattern is empty")
	}
}
