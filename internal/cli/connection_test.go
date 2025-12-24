package cli

import (
	"os"
	"testing"

	"github.com/vvka-141/pgmi/internal/db"
)

// TestResolveTargetDatabase tests the database precedence logic.
// The -d/--database flag should always take precedence over connection string database.
func TestResolveTargetDatabase(t *testing.T) {
	tests := []struct {
		name               string
		flagDatabase       string
		connConfigDatabase string
		requireDatabase    bool
		commandName        string
		verbose            bool
		wantDatabase       string
		wantErr            bool
	}{
		{
			name:               "flag database takes precedence over connection string",
			flagDatabase:       "myapp",
			connConfigDatabase: "postgres",
			requireDatabase:    true,
			commandName:        "deploy",
			verbose:            false,
			wantDatabase:       "myapp",
			wantErr:            false,
		},
		{
			name:               "use connection string database when flag not provided",
			flagDatabase:       "",
			connConfigDatabase: "myapp",
			requireDatabase:    true,
			commandName:        "deploy",
			verbose:            false,
			wantDatabase:       "myapp",
			wantErr:            false,
		},
		{
			name:               "error when no database provided and required",
			flagDatabase:       "",
			connConfigDatabase: "",
			requireDatabase:    true,
			commandName:        "deploy",
			verbose:            false,
			wantDatabase:       "",
			wantErr:            true,
		},
		{
			name:               "empty database allowed when not required",
			flagDatabase:       "",
			connConfigDatabase: "",
			requireDatabase:    false,
			commandName:        "deploy",
			verbose:            false,
			wantDatabase:       "",
			wantErr:            false,
		},
		{
			name:               "flag database overrides connection string (same name)",
			flagDatabase:       "myapp",
			connConfigDatabase: "myapp",
			requireDatabase:    true,
			commandName:        "deploy",
			verbose:            false,
			wantDatabase:       "myapp",
			wantErr:            false,
		},
		{
			name:               "verbose logging when flag overrides connection string",
			flagDatabase:       "override_db",
			connConfigDatabase: "original_db",
			requireDatabase:    true,
			commandName:        "test",
			verbose:            true,
			wantDatabase:       "override_db",
			wantErr:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDatabase, err := resolveTargetDatabase(
				tt.flagDatabase,
				tt.connConfigDatabase,
				tt.requireDatabase,
				tt.commandName,
				tt.verbose,
			)

			if (err != nil) != tt.wantErr {
				t.Errorf("resolveTargetDatabase() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if gotDatabase != tt.wantDatabase {
				t.Errorf("resolveTargetDatabase() = %v, want %v", gotDatabase, tt.wantDatabase)
			}
		})
	}
}

// TestDetermineMaintenanceDB tests the maintenance database selection logic.
func TestDetermineMaintenanceDB(t *testing.T) {
	tests := []struct {
		name                 string
		flagDatabase         string
		connStringDatabase   string
		currentMaintenanceDB string
		wantMaintenanceDB    string
	}{
		{
			name:                 "use postgres when database from connection string and not postgres",
			flagDatabase:         "",
			connStringDatabase:   "myapp",
			currentMaintenanceDB: "myapp",
			wantMaintenanceDB:    "postgres",
		},
		{
			name:                 "preserve maintenance DB when database from flag",
			flagDatabase:         "myapp",
			connStringDatabase:   "myapp",
			currentMaintenanceDB: "template1",
			wantMaintenanceDB:    "template1",
		},
		{
			name:                 "preserve maintenance DB when connection string is postgres",
			flagDatabase:         "",
			connStringDatabase:   "postgres",
			currentMaintenanceDB: "postgres",
			wantMaintenanceDB:    "postgres",
		},
		{
			name:                 "preserve maintenance DB when no connection string database",
			flagDatabase:         "",
			connStringDatabase:   "",
			currentMaintenanceDB: "postgres",
			wantMaintenanceDB:    "postgres",
		},
		{
			name:                 "use postgres for non-postgres connection string database",
			flagDatabase:         "",
			connStringDatabase:   "production_db",
			currentMaintenanceDB: "template0",
			wantMaintenanceDB:    "postgres",
		},
		{
			name:                 "preserve when flag overrides connection string",
			flagDatabase:         "override",
			connStringDatabase:   "original",
			currentMaintenanceDB: "maintenance",
			wantMaintenanceDB:    "maintenance",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMaintenanceDB := determineMaintenanceDB(
				tt.flagDatabase,
				tt.connStringDatabase,
				tt.currentMaintenanceDB,
			)

			if gotMaintenanceDB != tt.wantMaintenanceDB {
				t.Errorf("determineMaintenanceDB() = %v, want %v", gotMaintenanceDB, tt.wantMaintenanceDB)
			}
		})
	}
}

// TestResolveConnection_WithEnvironment tests connection resolution with environment variables.
// This test focuses on the PGMI_CONNECTION_STRING environment variable behavior.
func TestResolveConnection_WithEnvironment(t *testing.T) {
	// Save original environment
	originalEnv := os.Getenv("PGMI_CONNECTION_STRING")
	defer func() {
		if originalEnv != "" {
			os.Setenv("PGMI_CONNECTION_STRING", originalEnv)
		} else {
			os.Unsetenv("PGMI_CONNECTION_STRING")
		}
	}()

	tests := []struct {
		name           string
		connStringFlag string
		envConnString  string
		granularFlags  *db.GranularConnFlags
		wantHost       string
		wantErr        bool
	}{
		{
			name:           "flag takes precedence over environment",
			connStringFlag: "postgresql://user@localhost:5432/flagdb",
			envConnString:  "postgresql://user@envhost:5433/envdb",
			granularFlags:  &db.GranularConnFlags{},
			wantHost:       "localhost",
			wantErr:        false,
		},
		{
			name:           "use environment when flag not provided",
			connStringFlag: "",
			envConnString:  "postgresql://user@envhost:5433/envdb",
			granularFlags:  &db.GranularConnFlags{},
			wantHost:       "envhost",
			wantErr:        false,
		},
		{
			name:           "use defaults when neither flag nor env provided",
			connStringFlag: "",
			envConnString:  "",
			granularFlags:  &db.GranularConnFlags{},
			wantHost:       "localhost", // default from resolver
			wantErr:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup environment
			if tt.envConnString != "" {
				os.Setenv("PGMI_CONNECTION_STRING", tt.envConnString)
			} else {
				os.Unsetenv("PGMI_CONNECTION_STRING")
			}

			connConfig, _, err := resolveConnection(tt.connStringFlag, tt.granularFlags, nil, false)

			if (err != nil) != tt.wantErr {
				t.Errorf("resolveConnection() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && connConfig.Host != tt.wantHost {
				t.Errorf("resolveConnection() host = %v, want %v", connConfig.Host, tt.wantHost)
			}
		})
	}
}

// TestResolveConnection_GranularFlags tests connection resolution with granular CLI flags.
func TestResolveConnection_GranularFlags(t *testing.T) {
	// Clear environment to ensure clean test state
	originalEnv := os.Getenv("PGMI_CONNECTION_STRING")
	defer func() {
		if originalEnv != "" {
			os.Setenv("PGMI_CONNECTION_STRING", originalEnv)
		} else {
			os.Unsetenv("PGMI_CONNECTION_STRING")
		}
	}()
	os.Unsetenv("PGMI_CONNECTION_STRING")

	tests := []struct {
		name          string
		granularFlags *db.GranularConnFlags
		wantHost      string
		wantPort      int
		wantUsername  string
		wantDatabase  string
		wantSSLMode   string
		wantErr       bool
	}{
		{
			name: "all granular flags provided",
			granularFlags: &db.GranularConnFlags{
				Host:     "customhost",
				Port:     5433,
				Username: "customuser",
				Database: "customdb",
				SSLMode:  "require",
			},
			wantHost:     "customhost",
			wantPort:     5433,
			wantUsername: "customuser",
			wantDatabase: "customdb",
			wantSSLMode:  "require",
			wantErr:      false,
		},
		{
			name: "partial granular flags with defaults",
			granularFlags: &db.GranularConnFlags{
				Host:     "myhost",
				Database: "mydb",
			},
			wantHost:     "myhost",
			wantPort:     5432, // default
			wantDatabase: "mydb",
			wantErr:      false,
		},
		{
			name:          "no flags uses defaults",
			granularFlags: &db.GranularConnFlags{},
			wantHost:      "localhost", // default
			wantPort:      5432,        // default
			wantSSLMode:   "prefer",    // default
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connConfig, _, err := resolveConnection("", tt.granularFlags, nil, false)

			if (err != nil) != tt.wantErr {
				t.Errorf("resolveConnection() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if connConfig.Host != tt.wantHost {
					t.Errorf("resolveConnection() host = %v, want %v", connConfig.Host, tt.wantHost)
				}
				if tt.wantPort != 0 && connConfig.Port != tt.wantPort {
					t.Errorf("resolveConnection() port = %v, want %v", connConfig.Port, tt.wantPort)
				}
				if tt.wantUsername != "" && connConfig.Username != tt.wantUsername {
					t.Errorf("resolveConnection() username = %v, want %v", connConfig.Username, tt.wantUsername)
				}
				if tt.wantDatabase != "" && connConfig.Database != tt.wantDatabase {
					t.Errorf("resolveConnection() database = %v, want %v", connConfig.Database, tt.wantDatabase)
				}
				if tt.wantSSLMode != "" && connConfig.SSLMode != tt.wantSSLMode {
					t.Errorf("resolveConnection() sslmode = %v, want %v", connConfig.SSLMode, tt.wantSSLMode)
				}
			}
		})
	}
}

// TestResolveTargetDatabase_ErrorMessages tests that helpful error messages are returned.
func TestResolveTargetDatabase_ErrorMessages(t *testing.T) {
	_, err := resolveTargetDatabase("", "", true, "deploy", false)

	if err == nil {
		t.Fatal("expected error when no database provided, got nil")
	}

	// Verify error message contains helpful guidance
	errMsg := err.Error()
	expectedPhrases := []string{
		"database name is required",
		"--database/-d flag",
		"Connection string",
		"Environment variable",
	}

	for _, phrase := range expectedPhrases {
		if !contains(errMsg, phrase) {
			t.Errorf("error message missing expected phrase %q\nGot: %s", phrase, errMsg)
		}
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
