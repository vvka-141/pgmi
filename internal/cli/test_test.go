package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// resetTestFlags resets all test command-related global flags to their zero values.
// This is necessary because flags are package-level globals that persist across tests.
func resetTestFlags() {
	testConnection = ""
	testHost = ""
	testPort = 0
	testUsername = ""
	testDatabase = ""
	testSSLMode = ""
	testFilter = ""
	testList = false
	testParams = nil
	testParamsFile = ""
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
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = ".*"
				testList = false
				testParams = nil
				testParamsFile = ""
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
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = "/pre-deployment/"
				testList = false
				testParams = nil
				testParamsFile = ""
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
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = ".*"
				testList = true
				testParams = nil
				testParamsFile = ""
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
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = ".*"
				testList = false
				testParams = []string{"test_mode=fast", "verbose_output=true"}
				testParamsFile = ""
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
				testConnection = "postgresql://user:pass@testhost:5433/testdb"
				testDatabase = ""
				testHost = ""
				testPort = 0
				testUsername = ""
				testFilter = ".*"
				testList = false
				testParams = nil
				testParamsFile = ""
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
				testConnection = "postgresql://user:pass@testhost:5433/conndb"
				testDatabase = "override_db"
				testHost = ""
				testPort = 0
				testUsername = ""
				testFilter = ".*"
				testList = false
				testParams = nil
				testParamsFile = ""
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
				testConnection = ""
				testDatabase = ""
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = ".*"
				testList = false
				testParams = nil
				testParamsFile = ""
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "database name is required",
		},
		{
			name: "error with invalid CLI parameter format",
			setupFlags: func() {
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = ".*"
				testList = false
				testParams = []string{"invalid_param_no_equals"}
				testParamsFile = ""
			},
			sourcePath:      sourcePath,
			verbose:         false,
			wantErr:         true,
			wantErrContains: "invalid parameter format",
		},
		{
			name: "regex filter for auth tests",
			setupFlags: func() {
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = "^\\./pgitest/auth/"
				testList = false
				testParams = nil
				testParamsFile = ""
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
			config, err := buildTestConfig(tt.sourcePath, tt.verbose)

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
	testDatabase = "test_db"
	testHost = "localhost"
	testPort = 5432
	testUsername = "postgres"
	testFilter = ".*"
	testList = false
	testParamsFile = paramsFile
	testParams = []string{"test_mode=fast", "test_isolation=true"} // Override test_mode, add test_isolation

	// Build config
	config, err := buildTestConfig(tempDir, false)
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
				testConnection = "invalid://bad:format"
				testDatabase = "test_db"
				testHost = ""
				testPort = 0
				testUsername = ""
				testFilter = ".*"
				testList = false
				testParams = nil
				testParamsFile = ""
			},
			wantErrContains: "invalid connection string",
		},
		{
			name: "parameter missing equals sign",
			setupFlags: func() {
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = ".*"
				testList = false
				testParams = []string{"badparam"}
				testParamsFile = ""
			},
			wantErrContains: "invalid parameter format",
		},
		{
			name: "nonexistent params file",
			setupFlags: func() {
				testDatabase = "test_db"
				testHost = "localhost"
				testPort = 5432
				testUsername = "postgres"
				testFilter = ".*"
				testList = false
				testParams = nil
				testParamsFile = "/path/to/nonexistent/file.env"
			},
			wantErrContains: "failed to read params file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetTestFlags()
			tt.setupFlags()

			_, err := buildTestConfig(tempDir, false)

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
	testDatabase = "test_db"
	testHost = "localhost"
	testPort = 5432
	testUsername = "postgres"
	testFilter = ".*"
	testList = false
	testParams = []string{"env=test"}
	testParamsFile = ""

	config, err := buildTestConfig(tempDir, false)
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
