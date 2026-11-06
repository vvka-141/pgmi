package db

import (
	"slices"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
)

func TestEnvOverrides(t *testing.T) {
	pc := &config.ProjectConfig{Connection: config.ConnectionConfig{
		Host: "yamlhost", Port: 5434, Username: "yamluser", Database: "myapp"}}

	tests := []struct {
		name    string
		connStr string
		flags   *GranularConnFlags
		env     *EnvVars
		want    []string
	}{
		{"PGDATABASE over pgmi.yaml", "", nil, &EnvVars{PGDATABASE: "postgres"},
			[]string{"database: from PGDATABASE (postgres), not pgmi.yaml (myapp)"}},
		{"PGHOST, PGPORT and PGUSER over pgmi.yaml", "", nil,
			&EnvVars{PGHOST: "envhost", PGPORT: "6000", PGUSER: "envuser"},
			[]string{"host: from PGHOST (envhost), not pgmi.yaml (yamlhost)",
				"port: from PGPORT (6000), not pgmi.yaml (5434)",
				"user: from PGUSER (envuser), not pgmi.yaml (yamluser)"}},
		{"the same value is no override", "", nil, &EnvVars{PGHOST: "yamlhost", PGPORT: "5434"}, nil},
		{"a flag outranks both", "", &GranularConnFlags{Database: "flagdb", Host: "flaghost"},
			&EnvVars{PGDATABASE: "postgres", PGHOST: "envhost"}, nil},
		{"a dbname-less string: only the database", "postgresql://u@h", nil,
			&EnvVars{PGDATABASE: "postgres", PGHOST: "envhost"},
			[]string{"database: from PGDATABASE (postgres), not pgmi.yaml (myapp)"}},
		{"a string's database over pgmi.yaml", "postgresql://u@h/postgres", nil, &EnvVars{PGDATABASE: "other"},
			[]string{"database: from DATABASE_URL (postgres), not pgmi.yaml (myapp)"}},
		{"a string naming pgmi.yaml's database: no override", "postgresql://u@h/myapp", nil, &EnvVars{}, nil},
		{"-d outranks the string's database", "postgresql://u@h/postgres", &GranularConnFlags{Database: "myapp"}, &EnvVars{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overrideLines(EnvOverrides(tt.connStr, "DATABASE_URL", tt.flags, nil, tt.env, pc)); !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnvOverrides_TLS(t *testing.T) {
	pc := &config.ProjectConfig{Connection: config.ConnectionConfig{
		Host: "h", Database: "myapp", SSLMode: "verify-full",
		SSLCert: "yaml.crt", SSLKey: "yaml.key", SSLRootCert: "yaml-ca.crt"}}

	tests := []struct {
		name    string
		connStr string
		flags   *GranularConnFlags
		certs   *CertFlags
		env     *EnvVars
		want    []string
	}{
		{"PGSSLMODE over pgmi.yaml", "", nil, nil, &EnvVars{PGSSLMODE: "disable"},
			[]string{"sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)"}},
		{"--sslmode outranks PGSSLMODE", "", &GranularConnFlags{SSLMode: "require"}, nil,
			&EnvVars{PGSSLMODE: "disable"}, nil},
		{"certificate paths from PG* over pgmi.yaml", "", nil, nil,
			&EnvVars{PGSSLCERT: "env.crt", PGSSLKEY: "env.key", PGSSLROOTCERT: "env-ca.crt"},
			[]string{"sslcert: from PGSSLCERT (env.crt), not pgmi.yaml (yaml.crt)",
				"sslkey: from PGSSLKEY (env.key), not pgmi.yaml (yaml.key)",
				"sslrootcert: from PGSSLROOTCERT (env-ca.crt), not pgmi.yaml (yaml-ca.crt)"}},
		{"certificate flags outrank PG*", "", nil,
			&CertFlags{SSLCert: "f.crt", SSLKey: "f.key", SSLRootCert: "f-ca.crt"},
			&EnvVars{PGSSLCERT: "env.crt", PGSSLKEY: "env.key", PGSSLROOTCERT: "env-ca.crt"}, nil},
		{"a string without sslmode drops pgmi.yaml's", "postgresql://u@h/myapp", nil, nil, &EnvVars{},
			[]string{"sslmode: pgmi.yaml (verify-full) does not apply to the connection string from DATABASE_URL; set sslmode in the string"}},
		{"a string's sslmode over pgmi.yaml", "postgresql://u@h/myapp?sslmode=require", nil, nil, &EnvVars{},
			[]string{"sslmode: from DATABASE_URL (require), not pgmi.yaml (verify-full)"}},
		{"PGSSLMODE fills a string without sslmode", "postgresql://u@h/myapp", nil, nil, &EnvVars{PGSSLMODE: "disable"},
			[]string{"sslmode: from PGSSLMODE (disable), not pgmi.yaml (verify-full)"}},
		{"a string's certificate over pgmi.yaml", "postgresql://u@h/myapp?sslmode=verify-full&sslcert=s.crt", nil, nil, &EnvVars{},
			[]string{"sslcert: from DATABASE_URL (s.crt), not pgmi.yaml (yaml.crt)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overrideLines(EnvOverrides(tt.connStr, "DATABASE_URL", tt.flags, tt.certs, tt.env, pc)); !slices.Equal(got, tt.want) {
				t.Errorf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func overrideLines(overrides []Override) []string {
	var lines []string
	for _, o := range overrides {
		lines = append(lines, o.String())
	}
	return lines
}

// The scaffolded pgmi.yaml says sslmode: prefer, which is also what a string
// without sslmode gets, so nothing was replaced.
func TestEnvOverrides_DefaultSSLModeIsNoOverride(t *testing.T) {
	pc := &config.ProjectConfig{Connection: config.ConnectionConfig{Database: "myapp", SSLMode: "prefer"}}
	if got := EnvOverrides("postgresql://u@h/myapp", "PGMI_CONNECTION_STRING", nil, nil, &EnvVars{}, pc); got != nil {
		t.Errorf("got %q, want no note", overrideLines(got))
	}
}

func TestEnvOverrides_StringDatabaseFromEverySource(t *testing.T) {
	pc := &config.ProjectConfig{Connection: config.ConnectionConfig{Database: "mydb"}}
	for _, source := range []string{"--connection", "PGMI_CONNECTION_STRING", "DATABASE_URL"} {
		got := overrideLines(EnvOverrides("postgresql://u:p@127.0.0.1:5480/postgres", source, nil, nil, &EnvVars{}, pc))
		want := []string{"database: from " + source + " (postgres), not pgmi.yaml (mydb)"}
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", source, got, want)
		}
	}
}
