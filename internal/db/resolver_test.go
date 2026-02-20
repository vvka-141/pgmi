package db

import (
	"os"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/pkg/pgmi"
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
		"PGHOST":        os.Getenv("PGHOST"),
		"PGPORT":        os.Getenv("PGPORT"),
		"PGUSER":        os.Getenv("PGUSER"),
		"PGPASSWORD":    os.Getenv("PGPASSWORD"),
		"PGDATABASE":    os.Getenv("PGDATABASE"),
		"PGSSLMODE":     os.Getenv("PGSSLMODE"),
		"DATABASE_URL":  os.Getenv("DATABASE_URL"),
		"PGSSLCERT":     os.Getenv("PGSSLCERT"),
		"PGSSLKEY":      os.Getenv("PGSSLKEY"),
		"PGSSLROOTCERT": os.Getenv("PGSSLROOTCERT"),
		"PGSSLPASSWORD": os.Getenv("PGSSLPASSWORD"),
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
	os.Setenv("PGSSLCERT", "/path/client.crt")
	os.Setenv("PGSSLKEY", "/path/client.key")
	os.Setenv("PGSSLROOTCERT", "/path/ca.crt")
	os.Setenv("PGSSLPASSWORD", "keypass")

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
	if envVars.PGSSLCERT != "/path/client.crt" {
		t.Errorf("PGSSLCERT = %s, want /path/client.crt", envVars.PGSSLCERT)
	}
	if envVars.PGSSLKEY != "/path/client.key" {
		t.Errorf("PGSSLKEY = %s, want /path/client.key", envVars.PGSSLKEY)
	}
	if envVars.PGSSLROOTCERT != "/path/ca.crt" {
		t.Errorf("PGSSLROOTCERT = %s, want /path/ca.crt", envVars.PGSSLROOTCERT)
	}
	if envVars.PGSSLPASSWORD != "keypass" {
		t.Errorf("PGSSLPASSWORD = %s, want keypass", envVars.PGSSLPASSWORD)
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
			_, _, err := ResolveConnectionParams(tt.connString, tt.granularFlags, nil, nil, nil, nil, envVars, nil)

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
				nil, // Azure flags
				nil, // AWS flags
				nil, // Google flags
				nil, // Cert flags
				&EnvVars{},
				nil,
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
			config, maintenanceDB, err := ResolveConnectionParams("", tt.flags, nil, nil, nil, nil, tt.envVars, nil)
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
			config, maintenanceDB, err := ResolveConnectionParams("", tt.flags, nil, nil, nil, nil, tt.envVars, nil)
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

	_, _, err := ResolveConnectionParams("", flags, nil, nil, nil, nil, envVars, nil)
	if err == nil {
		t.Error("expected error for invalid PGPORT, got nil")
	}
}

func TestResolveConnectionParams_NilInputs(t *testing.T) {
	// Should not panic with nil inputs
	config, maintenanceDB, err := ResolveConnectionParams("", nil, nil, nil, nil, nil, nil, nil)
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

	config, _, err := ResolveConnectionParams("", flags, nil, nil, nil, nil, envVars, nil)
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

func TestResolveConnectionParams_ProjectConfig(t *testing.T) {
	pc := &config.ProjectConfig{
		Connection: config.ConnectionConfig{
			Host:     "yamlhost",
			Port:     5434,
			Username: "yamluser",
			Database: "yamldb",
			SSLMode:  "require",
		},
	}

	t.Run("project config used when no flags or env vars", func(t *testing.T) {
		cfg, _, err := ResolveConnectionParams("", &GranularConnFlags{}, nil, nil, nil, nil, &EnvVars{}, pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Host != "yamlhost" {
			t.Errorf("Host = %s, want yamlhost", cfg.Host)
		}
		if cfg.Port != 5434 {
			t.Errorf("Port = %d, want 5434", cfg.Port)
		}
		if cfg.Username != "yamluser" {
			t.Errorf("Username = %s, want yamluser", cfg.Username)
		}
		if cfg.Database != "yamldb" {
			t.Errorf("Database = %s, want yamldb", cfg.Database)
		}
		if cfg.SSLMode != "require" {
			t.Errorf("SSLMode = %s, want require", cfg.SSLMode)
		}
	})

	t.Run("env vars override project config", func(t *testing.T) {
		envVars := &EnvVars{
			PGHOST: "envhost",
			PGPORT: "5433",
		}
		cfg, _, err := ResolveConnectionParams("", &GranularConnFlags{}, nil, nil, nil, nil, envVars, pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Host != "envhost" {
			t.Errorf("Host = %s, want envhost", cfg.Host)
		}
		if cfg.Port != 5433 {
			t.Errorf("Port = %d, want 5433", cfg.Port)
		}
		if cfg.Username != "yamluser" {
			t.Errorf("Username = %s, want yamluser (from project config)", cfg.Username)
		}
	})

	t.Run("flags override project config and env vars", func(t *testing.T) {
		flags := &GranularConnFlags{Host: "flaghost"}
		cfg, _, err := ResolveConnectionParams("", flags, nil, nil, nil, nil, &EnvVars{}, pc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Host != "flaghost" {
			t.Errorf("Host = %s, want flaghost", cfg.Host)
		}
		if cfg.Port != 5434 {
			t.Errorf("Port = %d, want 5434 (from project config)", cfg.Port)
		}
	})
}

func TestCertFlags_IsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		flags *CertFlags
		want  bool
	}{
		{"nil", nil, true},
		{"empty struct", &CertFlags{}, true},
		{"SSLCert only", &CertFlags{SSLCert: "/path/client.crt"}, false},
		{"SSLKey only", &CertFlags{SSLKey: "/path/client.key"}, false},
		{"SSLRootCert only", &CertFlags{SSLRootCert: "/path/ca.crt"}, false},
		{"all fields", &CertFlags{SSLCert: "c", SSLKey: "k", SSLRootCert: "r"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.flags.IsEmpty(); got != tt.want {
				t.Errorf("CertFlags.IsEmpty() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyCertParams(t *testing.T) {
	tests := []struct {
		name            string
		flags           *CertFlags
		env             *EnvVars
		pc              *config.ProjectConfig
		wantSSLCert     string
		wantSSLKey      string
		wantSSLRootCert string
		wantSSLPassword string
	}{
		{
			name:  "all from flags",
			flags: &CertFlags{SSLCert: "flag.crt", SSLKey: "flag.key", SSLRootCert: "flag-ca.crt"},
			env:   &EnvVars{PGSSLCERT: "env.crt", PGSSLKEY: "env.key", PGSSLROOTCERT: "env-ca.crt"},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				SSLCert: "yaml.crt", SSLKey: "yaml.key", SSLRootCert: "yaml-ca.crt",
			}},
			wantSSLCert:     "flag.crt",
			wantSSLKey:      "flag.key",
			wantSSLRootCert: "flag-ca.crt",
		},
		{
			name:  "env overrides yaml",
			flags: &CertFlags{},
			env:   &EnvVars{PGSSLCERT: "env.crt", PGSSLKEY: "env.key"},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				SSLCert: "yaml.crt", SSLKey: "yaml.key", SSLRootCert: "yaml-ca.crt",
			}},
			wantSSLCert:     "env.crt",
			wantSSLKey:      "env.key",
			wantSSLRootCert: "yaml-ca.crt",
		},
		{
			name:  "yaml used when no flags or env",
			flags: &CertFlags{},
			env:   &EnvVars{},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				SSLCert: "yaml.crt", SSLKey: "yaml.key", SSLRootCert: "yaml-ca.crt",
			}},
			wantSSLCert:     "yaml.crt",
			wantSSLKey:      "yaml.key",
			wantSSLRootCert: "yaml-ca.crt",
		},
		{
			name:            "SSLPassword only from env",
			flags:           &CertFlags{},
			env:             &EnvVars{PGSSLPASSWORD: "env-pass"},
			pc:              nil,
			wantSSLPassword: "env-pass",
		},
		{
			name:  "nil flags and pc",
			flags: nil,
			env:   &EnvVars{PGSSLCERT: "env.crt"},
			pc:    nil,
			wantSSLCert: "env.crt",
		},
		{
			name:  "existing value preserved when no override",
			flags: nil,
			env:   &EnvVars{},
			pc:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &pgmi.ConnectionConfig{}
			applyCertParams(cfg, tt.flags, tt.env, tt.pc)

			if cfg.SSLCert != tt.wantSSLCert {
				t.Errorf("SSLCert = %q, want %q", cfg.SSLCert, tt.wantSSLCert)
			}
			if cfg.SSLKey != tt.wantSSLKey {
				t.Errorf("SSLKey = %q, want %q", cfg.SSLKey, tt.wantSSLKey)
			}
			if cfg.SSLRootCert != tt.wantSSLRootCert {
				t.Errorf("SSLRootCert = %q, want %q", cfg.SSLRootCert, tt.wantSSLRootCert)
			}
			if cfg.SSLPassword != tt.wantSSLPassword {
				t.Errorf("SSLPassword = %q, want %q", cfg.SSLPassword, tt.wantSSLPassword)
			}
		})
	}
}

func TestResolveConnectionParams_CertFlagsNoConflictWithConnection(t *testing.T) {
	certFlags := &CertFlags{
		SSLCert:     "/path/client.crt",
		SSLKey:      "/path/client.key",
		SSLRootCert: "/path/ca.crt",
	}

	cfg, _, err := ResolveConnectionParams(
		"postgresql://user@localhost/db",
		&GranularConnFlags{},
		nil, // Azure flags
		nil, // AWS flags
		nil, // Google flags
		certFlags,
		&EnvVars{},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.SSLCert != "/path/client.crt" {
		t.Errorf("SSLCert = %q, want /path/client.crt", cfg.SSLCert)
	}
	if cfg.SSLKey != "/path/client.key" {
		t.Errorf("SSLKey = %q, want /path/client.key", cfg.SSLKey)
	}
	if cfg.SSLRootCert != "/path/ca.crt" {
		t.Errorf("SSLRootCert = %q, want /path/ca.crt", cfg.SSLRootCert)
	}
}

func TestApplyAWSAuth(t *testing.T) {
	tests := []struct {
		name           string
		flags          *AWSFlags
		env            *EnvVars
		pc             *config.ProjectConfig
		wantAuthMethod pgmi.AuthMethod
		wantRegion     string
	}{
		{
			name:           "no AWS config",
			flags:          &AWSFlags{},
			env:            &EnvVars{},
			wantAuthMethod: pgmi.AuthMethodStandard,
		},
		{
			name:           "--aws with flag region",
			flags:          &AWSFlags{Enabled: true, Region: "us-east-1"},
			env:            &EnvVars{},
			wantAuthMethod: pgmi.AuthMethodAWSIAM,
			wantRegion:     "us-east-1",
		},
		{
			name:           "--aws with env region",
			flags:          &AWSFlags{Enabled: true},
			env:            &EnvVars{AWS_REGION: "eu-west-1"},
			wantAuthMethod: pgmi.AuthMethodAWSIAM,
			wantRegion:     "eu-west-1",
		},
		{
			name:           "--aws flag overrides env region",
			flags:          &AWSFlags{Enabled: true, Region: "us-east-1"},
			env:            &EnvVars{AWS_REGION: "eu-west-1"},
			wantAuthMethod: pgmi.AuthMethodAWSIAM,
			wantRegion:     "us-east-1",
		},
		{
			name:           "AWS_DEFAULT_REGION fallback",
			flags:          &AWSFlags{Enabled: true},
			env:            &EnvVars{AWS_DEFAULT_REGION: "ap-south-1"},
			wantAuthMethod: pgmi.AuthMethodAWSIAM,
			wantRegion:     "ap-south-1",
		},
		{
			name:  "pgmi.yaml activates AWS",
			flags: &AWSFlags{},
			env:   &EnvVars{},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				AuthMethod: "aws",
				AWSRegion:  "yaml-region",
			}},
			wantAuthMethod: pgmi.AuthMethodAWSIAM,
			wantRegion:     "yaml-region",
		},
		{
			name:  "env overrides pgmi.yaml region",
			flags: &AWSFlags{},
			env:   &EnvVars{AWS_REGION: "env-region"},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				AuthMethod: "aws",
				AWSRegion:  "yaml-region",
			}},
			wantAuthMethod: pgmi.AuthMethodAWSIAM,
			wantRegion:     "env-region",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &pgmi.ConnectionConfig{AuthMethod: pgmi.AuthMethodStandard}
			applyAWSAuth(cfg, tt.flags, tt.env, tt.pc)

			if cfg.AuthMethod != tt.wantAuthMethod {
				t.Errorf("AuthMethod = %v, want %v", cfg.AuthMethod, tt.wantAuthMethod)
			}
			if cfg.AWSRegion != tt.wantRegion {
				t.Errorf("AWSRegion = %v, want %v", cfg.AWSRegion, tt.wantRegion)
			}
		})
	}
}

func TestApplyGoogleAuth(t *testing.T) {
	tests := []struct {
		name           string
		flags          *GoogleFlags
		pc             *config.ProjectConfig
		wantAuthMethod pgmi.AuthMethod
		wantInstance   string
	}{
		{
			name:           "no Google config",
			flags:          &GoogleFlags{},
			wantAuthMethod: pgmi.AuthMethodStandard,
		},
		{
			name:           "--google with flag instance",
			flags:          &GoogleFlags{Enabled: true, Instance: "proj:region:inst"},
			wantAuthMethod: pgmi.AuthMethodGoogleIAM,
			wantInstance:   "proj:region:inst",
		},
		{
			name:  "pgmi.yaml activates Google",
			flags: &GoogleFlags{},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				AuthMethod:     "google",
				GoogleInstance: "yaml:region:inst",
			}},
			wantAuthMethod: pgmi.AuthMethodGoogleIAM,
			wantInstance:   "yaml:region:inst",
		},
		{
			name:  "flag overrides pgmi.yaml instance",
			flags: &GoogleFlags{Enabled: true, Instance: "flag:region:inst"},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				AuthMethod:     "google",
				GoogleInstance: "yaml:region:inst",
			}},
			wantAuthMethod: pgmi.AuthMethodGoogleIAM,
			wantInstance:   "flag:region:inst",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &pgmi.ConnectionConfig{AuthMethod: pgmi.AuthMethodStandard}
			applyGoogleAuth(cfg, tt.flags, tt.pc)

			if cfg.AuthMethod != tt.wantAuthMethod {
				t.Errorf("AuthMethod = %v, want %v", cfg.AuthMethod, tt.wantAuthMethod)
			}
			if cfg.GoogleInstance != tt.wantInstance {
				t.Errorf("GoogleInstance = %v, want %v", cfg.GoogleInstance, tt.wantInstance)
			}
		})
	}
}

func TestApplyAzureAuth_YamlFallback(t *testing.T) {
	tests := []struct {
		name             string
		flags            *AzureFlags
		env              *EnvVars
		pc               *config.ProjectConfig
		wantAuthMethod   pgmi.AuthMethod
		wantTenantID     string
		wantClientID     string
	}{
		{
			name:  "pgmi.yaml activates Azure",
			flags: &AzureFlags{},
			env:   &EnvVars{},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				AuthMethod:    "azure",
				AzureTenantID: "yaml-tenant",
				AzureClientID: "yaml-client",
			}},
			wantAuthMethod: pgmi.AuthMethodAzureEntraID,
			wantTenantID:   "yaml-tenant",
			wantClientID:   "yaml-client",
		},
		{
			name:  "env overrides pgmi.yaml tenant",
			flags: &AzureFlags{},
			env:   &EnvVars{AZURE_TENANT_ID: "env-tenant"},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				AuthMethod:    "azure",
				AzureTenantID: "yaml-tenant",
				AzureClientID: "yaml-client",
			}},
			wantAuthMethod: pgmi.AuthMethodAzureEntraID,
			wantTenantID:   "env-tenant",
			wantClientID:   "yaml-client",
		},
		{
			name:  "flag overrides both env and yaml",
			flags: &AzureFlags{Enabled: true, TenantID: "flag-tenant"},
			env:   &EnvVars{AZURE_TENANT_ID: "env-tenant"},
			pc: &config.ProjectConfig{Connection: config.ConnectionConfig{
				AuthMethod:    "azure",
				AzureTenantID: "yaml-tenant",
				AzureClientID: "yaml-client",
			}},
			wantAuthMethod: pgmi.AuthMethodAzureEntraID,
			wantTenantID:   "flag-tenant",
			wantClientID:   "yaml-client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &pgmi.ConnectionConfig{AuthMethod: pgmi.AuthMethodStandard}
			applyAzureAuth(cfg, tt.flags, tt.env, tt.pc)

			if cfg.AuthMethod != tt.wantAuthMethod {
				t.Errorf("AuthMethod = %v, want %v", cfg.AuthMethod, tt.wantAuthMethod)
			}
			if cfg.AzureTenantID != tt.wantTenantID {
				t.Errorf("AzureTenantID = %v, want %v", cfg.AzureTenantID, tt.wantTenantID)
			}
			if cfg.AzureClientID != tt.wantClientID {
				t.Errorf("AzureClientID = %v, want %v", cfg.AzureClientID, tt.wantClientID)
			}
		})
	}
}

func TestResolveConnectionParams_CertFlagsPrecedence(t *testing.T) {
	certFlags := &CertFlags{
		SSLCert: "flag.crt",
	}
	envVars := &EnvVars{
		PGSSLCERT:     "env.crt",
		PGSSLKEY:      "env.key",
		PGSSLROOTCERT: "env-ca.crt",
		PGSSLPASSWORD: "env-pass",
	}
	pc := &config.ProjectConfig{
		Connection: config.ConnectionConfig{
			SSLCert:     "yaml.crt",
			SSLKey:      "yaml.key",
			SSLRootCert: "yaml-ca.crt",
		},
	}

	cfg, _, err := ResolveConnectionParams(
		"",
		&GranularConnFlags{Host: "localhost"},
		nil, // Azure flags
		nil, // AWS flags
		nil, // Google flags
		certFlags,
		envVars,
		pc,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.SSLCert != "flag.crt" {
		t.Errorf("SSLCert = %q, want flag.crt (flag overrides env and yaml)", cfg.SSLCert)
	}
	if cfg.SSLKey != "env.key" {
		t.Errorf("SSLKey = %q, want env.key (env overrides yaml)", cfg.SSLKey)
	}
	if cfg.SSLRootCert != "env-ca.crt" {
		t.Errorf("SSLRootCert = %q, want env-ca.crt (env overrides yaml)", cfg.SSLRootCert)
	}
	if cfg.SSLPassword != "env-pass" {
		t.Errorf("SSLPassword = %q, want env-pass (env only)", cfg.SSLPassword)
	}
}
