package db

import (
	"testing"
)

// Additional edge case tests for connection resolver
// These complement the existing resolver_test.go with more corner cases

func TestResolveConnectionParams_PartialEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		envVars  *EnvVars
		wantHost string
		wantPort int
		wantUser string
	}{
		{
			name: "only PGHOST set",
			envVars: &EnvVars{
				PGHOST: "customhost",
			},
			wantHost: "customhost",
			wantPort: 5432, // default
			wantUser: "",   // will fallback to OS user
		},
		{
			name: "only PGPORT set",
			envVars: &EnvVars{
				PGPORT: "5433",
			},
			wantHost: "localhost", // default
			wantPort: 5433,
			wantUser: "", // will fallback to OS user
		},
		{
			name: "only PGUSER set",
			envVars: &EnvVars{
				PGUSER: "customuser",
			},
			wantHost: "localhost", // default
			wantPort: 5432,        // default
			wantUser: "customuser",
		},
		{
			name: "PGHOST and PGPORT",
			envVars: &EnvVars{
				PGHOST: "dbserver",
				PGPORT: "5434",
			},
			wantHost: "dbserver",
			wantPort: 5434,
			wantUser: "", // will fallback to OS user
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, maintenanceDB, err := ResolveConnectionParams(
				"",
				&GranularConnFlags{},
				nil,
				tt.envVars,
				nil,
			)

			if err != nil {
				t.Fatalf("ResolveConnectionParams failed: %v", err)
			}

			if config.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", config.Host, tt.wantHost)
			}

			if config.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", config.Port, tt.wantPort)
			}

			if tt.wantUser != "" && config.Username != tt.wantUser {
				t.Errorf("Username = %q, want %q", config.Username, tt.wantUser)
			}

			if maintenanceDB != "postgres" {
				t.Errorf("MaintenanceDB = %q, want %q", maintenanceDB, "postgres")
			}
		})
	}
}

func TestResolveConnectionParams_SSLModePrecedence(t *testing.T) {
	tests := []struct {
		name        string
		flags       *GranularConnFlags
		envVars     *EnvVars
		wantSSLMode string
	}{
		{
			name: "flag overrides env var",
			flags: &GranularConnFlags{
				SSLMode: "require",
			},
			envVars: &EnvVars{
				PGSSLMODE: "disable",
			},
			wantSSLMode: "require",
		},
		{
			name:  "env var used when no flag",
			flags: &GranularConnFlags{},
			envVars: &EnvVars{
				PGSSLMODE: "verify-full",
			},
			wantSSLMode: "verify-full",
		},
		{
			name:        "default when neither set",
			flags:       &GranularConnFlags{},
			envVars:     &EnvVars{},
			wantSSLMode: "prefer", // default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, _, err := ResolveConnectionParams(
				"",
				tt.flags,
				nil,
				tt.envVars,
				nil,
			)

			if err != nil {
				t.Fatalf("ResolveConnectionParams failed: %v", err)
			}

			if config.SSLMode != tt.wantSSLMode {
				t.Errorf("SSLMode = %q, want %q", config.SSLMode, tt.wantSSLMode)
			}
		})
	}
}

func TestResolveConnectionParams_DatabaseURL_Precedence(t *testing.T) {
	// DATABASE_URL should be used when no connection string and no granular flags
	tests := []struct {
		name        string
		connStr     string
		flags       *GranularConnFlags
		databaseURL string
		wantHost    string
		expectError bool
	}{
		{
			name:        "DATABASE_URL used when no other params",
			connStr:     "",
			flags:       &GranularConnFlags{},
			databaseURL: "postgresql://user:pass@dbhost:5433/mydb",
			wantHost:    "dbhost",
			expectError: false,
		},
		{
			name:    "connection string takes precedence over DATABASE_URL",
			connStr: "postgresql://user:pass@primary:5432/maindb",
			flags:   &GranularConnFlags{},
			databaseURL: "postgresql://user:pass@secondary:5433/backupdb",
			wantHost:    "primary", // connection string wins
			expectError: false,
		},
		{
			name:    "granular flags take precedence over DATABASE_URL",
			connStr: "",
			flags: &GranularConnFlags{
				Host: "flaghost",
			},
			databaseURL: "postgresql://user:pass@urlhost:5433/mydb",
			wantHost:    "flaghost", // granular flag wins
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envVars := &EnvVars{
				DATABASE_URL: tt.databaseURL,
			}

			config, _, err := ResolveConnectionParams(tt.connStr, tt.flags, nil, envVars, nil)

			if tt.expectError {
				if err == nil {
					t.Fatal("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if config.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", config.Host, tt.wantHost)
			}
		})
	}
}

func TestResolveConnectionParams_EmptyDatabaseInConnectionString(t *testing.T) {
	// When connection string has no database, should default to "postgres"
	config, maintenanceDB, err := ResolveConnectionParams(
		"postgresql://user:pass@localhost:5432",
		&GranularConnFlags{},
		nil,
		&EnvVars{},
		nil,
	)

	if err != nil {
		t.Fatalf("ResolveConnectionParams failed: %v", err)
	}

	if maintenanceDB != "postgres" {
		t.Errorf("MaintenanceDB = %q, want %q", maintenanceDB, "postgres")
	}

	// Config.Database might be empty, which is fine - caller will use maintenanceDB
	t.Logf("Config.Database = %q, MaintenanceDB = %q", config.Database, maintenanceDB)
}

func TestResolveConnectionParams_PGPORTEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		pgPort      string
		expectError bool
		wantPort    int
	}{
		{
			name:        "valid port",
			pgPort:      "5433",
			expectError: false,
			wantPort:    5433,
		},
		{
			name:        "empty string uses default",
			pgPort:      "",
			expectError: false,
			wantPort:    5432, // default
		},
		{
			name:        "invalid - non-numeric",
			pgPort:      "abc",
			expectError: true,
		},
		{
			name:        "invalid - negative",
			pgPort:      "-1",
			expectError: false, // strconv.Atoi accepts negative, but PostgreSQL won't
			wantPort:    -1,
		},
		{
			name:        "invalid - too large",
			pgPort:      "999999",
			expectError: false, // strconv.Atoi accepts, but PostgreSQL won't
			wantPort:    999999,
		},
		{
			name:        "invalid - with spaces",
			pgPort:      " 5432 ",
			expectError: true, // strconv.Atoi will fail
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envVars := &EnvVars{
				PGPORT: tt.pgPort,
			}

			config, _, err := ResolveConnectionParams(
				"",
				&GranularConnFlags{},
				nil,
				envVars,
				nil,
			)

			if tt.expectError {
				if err == nil {
					t.Fatal("Expected error for invalid PGPORT, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if config.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", config.Port, tt.wantPort)
			}
		})
	}
}

func TestResolveConnectionParams_PasswordFromEnvOnly(t *testing.T) {
	// Password should ONLY come from PGPASSWORD env var, never from flags
	envVars := &EnvVars{
		PGPASSWORD: "secretpass",
	}

	config, _, err := ResolveConnectionParams(
		"",
		&GranularConnFlags{},
		nil,
		envVars,
		nil,
	)

	if err != nil {
		t.Fatalf("ResolveConnectionParams failed: %v", err)
	}

	if config.Password != "secretpass" {
		t.Errorf("Password = %q, want %q", config.Password, "secretpass")
	}
}

func TestResolveConnectionParams_MaintenanceDatabaseDifference(t *testing.T) {
	// Connection string vs granular flags should differ in maintenance DB
	tests := []struct {
		name                string
		connStr             string
		flags               *GranularConnFlags
		expectedMaintenance string
	}{
		{
			name:                "connection string - uses database from URL",
			connStr:             "postgresql://user@localhost/mydb",
			flags:               &GranularConnFlags{},
			expectedMaintenance: "mydb",
		},
		{
			name:                "granular flags - always uses postgres",
			connStr:             "",
			flags:               &GranularConnFlags{Host: "localhost"},
			expectedMaintenance: "postgres",
		},
		{
			name:                "connection string without database - defaults to postgres",
			connStr:             "postgresql://user@localhost",
			flags:               &GranularConnFlags{},
			expectedMaintenance: "postgres",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, maintenanceDB, err := ResolveConnectionParams(
				tt.connStr,
				tt.flags,
				nil,
				&EnvVars{},
				nil,
			)

			if err != nil {
				t.Fatalf("ResolveConnectionParams failed: %v", err)
			}

			if maintenanceDB != tt.expectedMaintenance {
				t.Errorf("MaintenanceDB = %q, want %q", maintenanceDB, tt.expectedMaintenance)
			}
		})
	}
}
