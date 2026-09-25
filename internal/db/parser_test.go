package db

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestParseConnectionString_PostgreSQLURI(t *testing.T) {
	tests := []struct {
		name    string
		connStr string
		want    *pgmi.ConnectionConfig
		wantErr bool
	}{
		{
			name:    "Full URI with all components",
			connStr: "postgresql://user:pass@localhost:5432/mydb?sslmode=disable",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				Password:         "pass",
				SSLMode:          "disable",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "URI without password",
			connStr: "postgresql://user@localhost:5432/mydb",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				Password:         "",
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "URI with default values",
			connStr: "postgresql://",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "postgres",
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "URI with custom port",
			connStr: "postgresql://localhost:5433/mydb",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5433,
				Database:         "mydb",
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "URI with application_name",
			connStr: "postgresql://localhost:5432/mydb?application_name=pgmi",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				SSLMode:          "",
				AppName:          "pgmi",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "URI with SSL cert params",
			connStr: "postgresql://user@localhost:5432/mydb?sslmode=verify-full&sslcert=/path/client.crt&sslkey=/path/client.key&sslrootcert=/path/ca.crt&sslpassword=keypass",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				SSLMode:          "verify-full",
				SSLCert:          "/path/client.crt",
				SSLKey:           "/path/client.key",
				SSLRootCert:      "/path/ca.crt",
				SSLPassword:      "keypass",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConnectionString(tt.connStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseConnectionString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				compareConfigs(t, got, tt.want)
			}
		})
	}
}

func TestParseConnectionString_ADONET(t *testing.T) {
	tests := []struct {
		name    string
		connStr string
		want    *pgmi.ConnectionConfig
		wantErr bool
	}{
		{
			name:    "Full ADO.NET connection string",
			connStr: "Host=localhost;Port=5433;Database=postgres;Username=postgres;Password=postgres",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5433,
				Database:         "postgres",
				Username:         "postgres",
				Password:         "postgres",
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "ADO.NET with quoted password containing a semicolon",
			connStr: "Host=localhost;Database=mydb;Username=user;Password='p;a=ss'",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				Password:         "p;a=ss",
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "ADO.NET with double-quoted password and doubled quote",
			connStr: `Host=localhost;Database=mydb;Username=user;Password="a""b;c"`,
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				Password:         `a"b;c`,
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "ADO.NET with Server instead of Host",
			connStr: "Server=localhost;Port=5432;Database=mydb;User Id=user;Pwd=pass",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				Password:         "pass",
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "ADO.NET with SSL Mode",
			connStr: "Host=localhost;Database=mydb;Username=user;SSL Mode=require",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				SSLMode:          "require",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "ADO.NET with spaces and case variations",
			connStr: "Host = localhost ; Port = 5432 ; Database = mydb ; Username = user",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				SSLMode:          "",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "ADO.NET with SSL cert params",
			connStr: "Host=localhost;Database=mydb;Username=user;sslcert=/path/client.crt;sslkey=/path/client.key;sslrootcert=/path/ca.crt;sslpassword=keypass",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				Username:         "user",
				SSLCert:          "/path/client.crt",
				SSLKey:           "/path/client.key",
				SSLRootCert:      "/path/ca.crt",
				SSLPassword:      "keypass",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:    "ADO.NET with spaced SSL cert params",
			connStr: "Host=localhost;Database=mydb;SSL Cert=/path/client.crt;SSL Key=/path/client.key;SSL Root Cert=/path/ca.crt;SSL Password=keypass",
			want: &pgmi.ConnectionConfig{
				Host:             "localhost",
				Port:             5432,
				Database:         "mydb",
				SSLCert:          "/path/client.crt",
				SSLKey:           "/path/client.key",
				SSLRootCert:      "/path/ca.crt",
				SSLPassword:      "keypass",
				AuthMethod:       pgmi.AuthMethodStandard,
				AdditionalParams: map[string]string{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConnectionString(tt.connStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseConnectionString() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				compareConfigs(t, got, tt.want)
			}
		})
	}
}

func TestParseConnectionString_Errors(t *testing.T) {
	tests := []struct {
		name    string
		connStr string
	}{
		{
			name:    "Empty string",
			connStr: "",
		},
		{
			name:    "Invalid URI port",
			connStr: "postgresql://localhost:abc/mydb",
		},
		{
			name:    "Invalid ADO.NET port",
			connStr: "Host=localhost;Port=abc;Database=mydb",
		},
		{
			name:    "Invalid URI connect_timeout",
			connStr: "postgresql://localhost/mydb?connect_timeout=five",
		},
		{
			name:    "Invalid ADO.NET timeout",
			connStr: "Host=localhost;Database=mydb;Timeout=abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConnectionString(tt.connStr)
			if err == nil {
				t.Errorf("ParseConnectionString() expected error for input: %s", tt.connStr)
			}
		})
	}
}

func TestBuildConnectionString(t *testing.T) {
	config := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5433,
		Database: "mydb",
		Username: "user",
		Password: "pass",
		SSLMode:  "disable",
	}

	connStr := BuildConnectionString(config)

	// Parse it back to verify round-trip
	parsed, err := ParseConnectionString(connStr)
	if err != nil {
		t.Fatalf("BuildConnectionString() produced invalid string: %v", err)
	}

	compareConfigs(t, parsed, config)
}

func TestBuildConnectionString_HostForms(t *testing.T) {
	for _, host := range []string{"::1", "2001:db8::5", "/var/run/postgresql", "db.example.com"} {
		t.Run(host, func(t *testing.T) {
			config := &pgmi.ConnectionConfig{Host: host, Port: 5471, Database: "mydb", Username: "user"}
			connStr := BuildConnectionString(config)

			parsed, err := ParseConnectionString(connStr)
			if err != nil {
				t.Fatalf("round trip of %q: %v", connStr, err)
			}
			compareConfigs(t, parsed, config)

			pgxCfg, err := pgx.ParseConfig(connStr)
			if err != nil {
				t.Fatalf("pgx rejects %q: %v", connStr, err)
			}
			if pgxCfg.Host != host || pgxCfg.Port != 5471 {
				t.Errorf("pgx dials %s port %d from %q, want %s port 5471", pgxCfg.Host, pgxCfg.Port, connStr, host)
			}
		})
	}
}

func TestBuildConnectionString_WithCertParams(t *testing.T) {
	config := &pgmi.ConnectionConfig{
		Host:        "localhost",
		Port:        5432,
		Database:    "mydb",
		Username:    "user",
		SSLMode:     "verify-full",
		SSLCert:     "/path/client.crt",
		SSLKey:      "/path/client.key",
		SSLRootCert: "/path/ca.crt",
		SSLPassword: "keypass",
	}

	connStr := BuildConnectionString(config)

	parsed, err := ParseConnectionString(connStr)
	if err != nil {
		t.Fatalf("BuildConnectionString() produced invalid string: %v", err)
	}

	compareConfigs(t, parsed, config)
}

func compareConfigs(t *testing.T, got, want *pgmi.ConnectionConfig) {
	t.Helper()

	if got.Host != want.Host {
		t.Errorf("Host = %v, want %v", got.Host, want.Host)
	}
	if got.Port != want.Port {
		t.Errorf("Port = %v, want %v", got.Port, want.Port)
	}
	if got.Database != want.Database {
		t.Errorf("Database = %v, want %v", got.Database, want.Database)
	}
	if got.Username != want.Username {
		t.Errorf("Username = %v, want %v", got.Username, want.Username)
	}
	if got.Password != want.Password {
		t.Errorf("Password = %v, want %v", got.Password, want.Password)
	}
	if got.SSLMode != want.SSLMode {
		t.Errorf("SSLMode = %v, want %v", got.SSLMode, want.SSLMode)
	}
	if got.AppName != want.AppName {
		t.Errorf("AppName = %v, want %v", got.AppName, want.AppName)
	}
	if got.SSLCert != want.SSLCert {
		t.Errorf("SSLCert = %v, want %v", got.SSLCert, want.SSLCert)
	}
	if got.SSLKey != want.SSLKey {
		t.Errorf("SSLKey = %v, want %v", got.SSLKey, want.SSLKey)
	}
	if got.SSLRootCert != want.SSLRootCert {
		t.Errorf("SSLRootCert = %v, want %v", got.SSLRootCert, want.SSLRootCert)
	}
	if got.SSLPassword != want.SSLPassword {
		t.Errorf("SSLPassword = %v, want %v", got.SSLPassword, want.SSLPassword)
	}
}

// A primary/standby pair is written as one URI listing both hosts. pgmi parses
// and rebuilds the connection string, and used to turn
// h1:5432,h2:5433 into the invalid [h1:5432,h2]:5433.
func TestConnectionString_MultiHostRoundTrip(t *testing.T) {
	for _, in := range []string{
		"postgresql://u:p@db1:5432,db2:5433/app?target_session_attrs=read-write",
		"postgresql://u@db1,db2:5433/app",
	} {
		c, err := ParseConnectionString(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		out := BuildConnectionString(c)
		cfg, err := pgconn.ParseConfig(out)
		if err != nil {
			t.Fatalf("%s rebuilt as %s, which pgx rejects: %v", in, out, err)
		}
		want, _ := pgconn.ParseConfig(in)
		got := []string{fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)}
		for _, fb := range cfg.Fallbacks {
			got = append(got, fmt.Sprintf("%s:%d", fb.Host, fb.Port))
		}
		exp := []string{fmt.Sprintf("%s:%d", want.Host, want.Port)}
		for _, fb := range want.Fallbacks {
			exp = append(exp, fmt.Sprintf("%s:%d", fb.Host, fb.Port))
		}
		if strings.Join(got, ",") != strings.Join(exp, ",") {
			t.Errorf("%s rebuilt as %s: hosts %v, want %v", in, out, got, exp)
		}
	}
}

// The libpq keyword/value form is what psql, the PostgreSQL docs and many
// hosting dashboards hand out. It was rejected as "unrecognized".
func TestParseConnectionString_KeywordValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want pgmi.ConnectionConfig
	}{
		{"plain", "host=db1 port=5433 dbname=app user=me password=secret sslmode=require",
			pgmi.ConnectionConfig{Host: "db1", Port: 5433, Database: "app", Username: "me", Password: "secret", SSLMode: "require"}},
		{"spaces around equals and defaults", "host = db1   dbname = app",
			pgmi.ConnectionConfig{Host: "db1", Port: 5432, Database: "app"}},
		{"quoted value with space, quote and semicolon", `host=db1 password='a b\'c;d' dbname=app`,
			pgmi.ConnectionConfig{Host: "db1", Port: 5432, Database: "app", Password: "a b'c;d"}},
		{"host list with one port", "host=db1,db2 port=5433 dbname=app",
			pgmi.ConnectionConfig{Host: "db1:5433,db2:5433", Port: 5432, Database: "app"}},
		{"host list with a port each", "host=db1,db2 port=5432,5433 dbname=app",
			pgmi.ConnectionConfig{Host: "db1:5432,db2:5433", Port: 5432, Database: "app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseConnectionString(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.Host != tt.want.Host || got.Port != tt.want.Port || got.Database != tt.want.Database ||
				got.Username != tt.want.Username || got.Password != tt.want.Password || got.SSLMode != tt.want.SSLMode {
				t.Errorf("got host=%q port=%d db=%q user=%q pass=%q ssl=%q", got.Host, got.Port, got.Database, got.Username, got.Password, got.SSLMode)
			}
			if _, err := pgconn.ParseConfig(BuildConnectionString(got)); err != nil {
				t.Errorf("rebuilt string is not accepted by pgx: %v", err)
			}
		})
	}

	for _, bad := range []string{"host=db1 dbname", "host=db1 password='unterminated", "host=a,b,c port=1,2"} {
		if _, err := ParseConnectionString(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}

	ado, err := ParseConnectionString("Host=db1;Database=app;Username=me")
	if err != nil || ado.Host != "db1" || ado.Database != "app" {
		t.Errorf("ADO.NET form must still parse: %+v, %v", ado, err)
	}
}
