package db

import (
	"os"
	"testing"
)

func TestGranularConnFlags_IsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		flags GranularConnFlags
		want  bool
	}{
		{
			name:  "empty flags",
			flags: GranularConnFlags{},
			want:  true,
		},
		{
			name:  "only host set",
			flags: GranularConnFlags{Host: "localhost"},
			want:  false,
		},
		{
			name:  "only port set",
			flags: GranularConnFlags{Port: 5432},
			want:  false,
		},
		{
			name:  "only username set",
			flags: GranularConnFlags{Username: "testuser"},
			want:  false,
		},
		{
			name:  "only database set",
			flags: GranularConnFlags{Database: "testdb"},
			want:  true, // Database is excluded from IsEmpty() check (can be used with connection string)
		},
		{
			name:  "only sslmode set",
			flags: GranularConnFlags{SSLMode: "require"},
			want:  false,
		},
		{
			name: "all fields set",
			flags: GranularConnFlags{
				Host:     "localhost",
				Port:     5432,
				Username: "testuser",
				Database: "testdb",
				SSLMode:  "require",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.flags.IsEmpty()
			if got != tt.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	// Save current env and restore after test
	originalEnv := map[string]string{
		"PGHOST":       os.Getenv("PGHOST"),
		"PGPORT":       os.Getenv("PGPORT"),
		"PGUSER":       os.Getenv("PGUSER"),
		"PGPASSWORD":   os.Getenv("PGPASSWORD"),
		"PGDATABASE":   os.Getenv("PGDATABASE"),
		"PGSSLMODE":    os.Getenv("PGSSLMODE"),
		"DATABASE_URL": os.Getenv("DATABASE_URL"),
	}
	defer func() {
		for key, val := range originalEnv {
			if val == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, val)
			}
		}
	}()

	// Clear all PG env vars
	for key := range originalEnv {
		os.Unsetenv(key)
	}

	// Set test values
	os.Setenv("PGHOST", "testhost")
	os.Setenv("PGPORT", "5433")
	os.Setenv("PGUSER", "testuser")
	os.Setenv("PGPASSWORD", "testpass")
	os.Setenv("PGDATABASE", "testdb")
	os.Setenv("PGSSLMODE", "require")
	os.Setenv("DATABASE_URL", "postgresql://user@host/db")

	envVars := LoadFromEnvironment()

	if envVars.PGHOST != "testhost" {
		t.Errorf("PGHOST = %s, want testhost", envVars.PGHOST)
	}
	if envVars.PGPORT != "5433" {
		t.Errorf("PGPORT = %s, want 5433", envVars.PGPORT)
	}
	if envVars.PGUSER != "testuser" {
		t.Errorf("PGUSER = %s, want testuser", envVars.PGUSER)
	}
	if envVars.PGPASSWORD != "testpass" {
		t.Errorf("PGPASSWORD = %s, want testpass", envVars.PGPASSWORD)
	}
	if envVars.PGDATABASE != "testdb" {
		t.Errorf("PGDATABASE = %s, want testdb", envVars.PGDATABASE)
	}
	if envVars.PGSSLMODE != "require" {
		t.Errorf("PGSSLMODE = %s, want require", envVars.PGSSLMODE)
	}
	if envVars.DATABASE_URL != "postgresql://user@host/db" {
		t.Errorf("DATABASE_URL = %s, want postgresql://user@host/db", envVars.DATABASE_URL)
	}
}

func TestResolveConnectionParams_ConflictDetection(t *testing.T) {
	tests := []struct {
		name          string
		connString    string
		granularFlags *GranularConnFlags
		wantError     bool
	}{
		{
			name:          "connection string only - no conflict",
			connString:    "postgresql://user@localhost/db",
			granularFlags: &GranularConnFlags{},
			wantError:     false,
		},
		{
			name:       "granular flags only - no conflict",
			connString: "",
			granularFlags: &GranularConnFlags{
				Host: "localhost",
			},
			wantError: false,
		},
		{
			name:       "connection string + host flag - conflict",
			connString: "postgresql://user@localhost/db",
			granularFlags: &GranularConnFlags{
				Host: "otherhost",
			},
			wantError: true,
		},
		{
			name:       "connection string + port flag - conflict",
			connString: "postgresql://user@localhost/db",
			granularFlags: &GranularConnFlags{
				Port: 5433,
			},
			wantError: true,
		},
		{
			name:       "connection string + database flag - no conflict (database can override)",
			connString: "postgresql://user@localhost/db",
			granularFlags: &GranularConnFlags{
				Database: "otherdb",
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envVars := &EnvVars{}
			_, _, err := ResolveConnectionParams(tt.connString, tt.granularFlags, nil, envVars)

			if tt.wantError && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveConnectionParams_FromConnectionString(t *testing.T) {
	tests := []struct {
		name              string
		connString        string
		wantHost          string
		wantPort          int
		wantDatabase      string
		wantMaintenanceDB string
		wantError         bool
	}{
		{
			name:              "full URI",
			connString:        "postgresql://testuser:testpass@testhost:5433/testdb",
			wantHost:          "testhost",
			wantPort:          5433,
			wantDatabase:      "testdb",
			wantMaintenanceDB: "testdb",
			wantError:         false,
		},
		{
			name:              "URI with defaults",
			connString:        "postgresql://localhost/postgres",
			wantHost:          "localhost",
			wantPort:          5432,
			wantDatabase:      "postgres",
			wantMaintenanceDB: "postgres",
			wantError:         false,
		},
		{
			name:              "URI without database - uses default",
			connString:        "postgresql://testuser@testhost:5433",
			wantHost:          "testhost",
			wantPort:          5433,
			wantDatabase:      "postgres",
			wantMaintenanceDB: "postgres",
			wantError:         false,
		},
		{
			name:       "invalid URI",
			connString: "not-a-valid-uri",
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, maintenanceDB, err := ResolveConnectionParams(
				tt.connString,
				&GranularConnFlags{},
				nil,
				&EnvVars{},
			)

			if tt.wantError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if config.Host != tt.wantHost {
				t.Errorf("Host = %s, want %s", config.Host, tt.wantHost)
			}
			if config.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", config.Port, tt.wantPort)
			}
			if config.Database != tt.wantDatabase {
				t.Errorf("Database = %s, want %s", config.Database, tt.wantDatabase)
			}
			if maintenanceDB != tt.wantMaintenanceDB {
				t.Errorf("maintenanceDB = %s, want %s", maintenanceDB, tt.wantMaintenanceDB)
			}
		})
	}
}

func TestResolveConnectionParams_FromGranularFlags(t *testing.T) {
	tests := []struct {
		name         string
		flags        *GranularConnFlags
		envVars      *EnvVars
		wantHost     string
		wantPort     int
		wantUsername string
		wantDatabase string
		wantSSLMode  string
		wantMaintDB  string
	}{
		{
			name: "all flags provided",
			flags: &GranularConnFlags{
				Host:     "flaghost",
				Port:     5433,
				Username: "flaguser",
				Database: "flagdb",
				SSLMode:  "require",
			},
			envVars:      &EnvVars{},
			wantHost:     "flaghost",
			wantPort:     5433,
			wantUsername: "flaguser",
			wantDatabase: "flagdb",
			wantSSLMode:  "require",
			wantMaintDB:  "postgres",
		},
		{
			name:  "flags override env vars",
			flags: &GranularConnFlags{Host: "flaghost"},
			envVars: &EnvVars{
				PGHOST: "envhost",
				PGPORT: "5433",
			},
			wantHost:    "flaghost",
			wantPort:    5433,
			wantSSLMode: "prefer",
			wantMaintDB: "postgres",
		},
		{
			name:  "env vars used when flags empty",
			flags: &GranularConnFlags{},
			envVars: &EnvVars{
				PGHOST:     "envhost",
				PGPORT:     "5433",
				PGUSER:     "envuser",
				PGDATABASE: "envdb",
				PGSSLMODE:  "require",
			},
			wantHost:     "envhost",
			wantPort:     5433,
			wantUsername: "envuser",
			wantDatabase: "envdb",
			wantSSLMode:  "require",
			wantMaintDB:  "postgres",
		},
		{
			name:        "defaults used when no flags or env vars",
			flags:       &GranularConnFlags{},
			envVars:     &EnvVars{},
			wantHost:    "localhost",
			wantPort:    5432,
			wantSSLMode: "prefer",
			wantMaintDB: "postgres",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, maintenanceDB, err := ResolveConnectionParams("", tt.flags, nil, tt.envVars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if config.Host != tt.wantHost {
				t.Errorf("Host = %s, want %s", config.Host, tt.wantHost)
			}
			if config.Port != tt.wantPort {
				t.Errorf("Port = %d, want %d", config.Port, tt.wantPort)
			}
			if tt.wantUsername != "" && config.Username != tt.wantUsername {
				t.Errorf("Username = %s, want %s", config.Username, tt.wantUsername)
			}
			if tt.wantDatabase != "" && config.Database != tt.wantDatabase {
				t.Errorf("Database = %s, want %s", config.Database, tt.wantDatabase)
			}
			if config.SSLMode != tt.wantSSLMode {
				t.Errorf("SSLMode = %s, want %s", config.SSLMode, tt.wantSSLMode)
			}
			if maintenanceDB != tt.wantMaintDB {
				t.Errorf("maintenanceDB = %s, want %s", maintenanceDB, tt.wantMaintDB)
			}
		})
	}
}

func TestResolveConnectionParams_DatabaseURL(t *testing.T) {
	tests := []struct {
		name         string
		flags        *GranularConnFlags
		envVars      *EnvVars
		wantHost     string
		wantDatabase string
		wantMaintDB  string
	}{
		{
			name:  "DATABASE_URL used when no flags",
			flags: &GranularConnFlags{},
			envVars: &EnvVars{
				DATABASE_URL: "postgresql://user:pass@dbhost:5433/mydb",
			},
			wantHost:     "dbhost",
			wantDatabase: "mydb",
			wantMaintDB:  "mydb",
		},
		{
			name: "granular flags override DATABASE_URL",
			flags: &GranularConnFlags{
				Host: "flaghost",
			},
			envVars: &EnvVars{
				DATABASE_URL: "postgresql://user:pass@envhost:5433/envdb",
			},
			wantHost:    "flaghost",
			wantMaintDB: "postgres",
		},
		{
			name:  "PGHOST overrides DATABASE_URL when granular flag present",
			flags: &GranularConnFlags{Port: 5433},
			envVars: &EnvVars{
				PGHOST:       "pghost",
				DATABASE_URL: "postgresql://user:pass@urlhost:5432/urldb",
			},
			wantHost:    "pghost",
			wantMaintDB: "postgres",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, maintenanceDB, err := ResolveConnectionParams("", tt.flags, nil, tt.envVars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if config.Host != tt.wantHost {
				t.Errorf("Host = %s, want %s", config.Host, tt.wantHost)
			}
			if tt.wantDatabase != "" && config.Database != tt.wantDatabase {
				t.Errorf("Database = %s, want %s", config.Database, tt.wantDatabase)
			}
			if maintenanceDB != tt.wantMaintDB {
				t.Errorf("maintenanceDB = %s, want %s", maintenanceDB, tt.wantMaintDB)
			}
		})
	}
}

func TestResolveConnectionParams_InvalidPGPORT(t *testing.T) {
	flags := &GranularConnFlags{}
	envVars := &EnvVars{
		PGPORT: "not-a-number",
	}

	_, _, err := ResolveConnectionParams("", flags, nil, envVars)
	if err == nil {
		t.Error("expected error for invalid PGPORT, got nil")
	}
}

func TestResolveConnectionParams_NilInputs(t *testing.T) {
	// Should not panic with nil inputs
	config, maintenanceDB, err := ResolveConnectionParams("", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should use defaults
	if config.Host != "localhost" {
		t.Errorf("Host = %s, want localhost", config.Host)
	}
	if config.Port != 5432 {
		t.Errorf("Port = %d, want 5432", config.Port)
	}
	if maintenanceDB != "postgres" {
		t.Errorf("maintenanceDB = %s, want postgres", maintenanceDB)
	}
}

func TestResolveConnectionParams_Precedence(t *testing.T) {
	// Test complete precedence chain: flags > env vars > defaults
	flags := &GranularConnFlags{
		Host: "flaghost", // Flag overrides env var
		// Port not set - should use env var
		// Username not set - should use default
	}

	envVars := &EnvVars{
		PGHOST: "envhost", // Should be ignored (flag takes precedence)
		PGPORT: "5433",    // Should be used (no flag)
		PGUSER: "envuser", // Should be used (no flag)
	}

	config, _, err := ResolveConnectionParams("", flags, nil, envVars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if config.Host != "flaghost" {
		t.Errorf("Host = %s, want flaghost (flag should override env)", config.Host)
	}
	if config.Port != 5433 {
		t.Errorf("Port = %d, want 5433 (from env var)", config.Port)
	}
	if config.Username != "envuser" {
		t.Errorf("Username = %s, want envuser (from env var)", config.Username)
	}
}
