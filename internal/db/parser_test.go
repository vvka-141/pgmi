package db

import (
	"errors"
	"fmt"
	"net"
	"strconv"
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
		want    *ConnectionConfig
		wantErr bool
	}{
		{
			name:    "Full URI with all components",
			connStr: "postgresql://user:pass@localhost:5432/mydb?sslmode=disable",
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
		want    *ConnectionConfig
		wantErr bool
	}{
		{
			name:    "Full ADO.NET connection string",
			connStr: "Host=localhost;Port=5433;Database=postgres;Username=postgres;Password=postgres",
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
			want: &ConnectionConfig{
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
	config := &ConnectionConfig{
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
			config := &ConnectionConfig{Host: host, Port: 5471, Database: "mydb", Username: "user"}
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
	config := &ConnectionConfig{
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

func compareConfigs(t *testing.T, got, want *ConnectionConfig) {
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

// A host list without per-host ports takes the port from ?port=, port=, PGPORT
// or -p. pgmi dropped it and dialed 5432 on every host, so a deploy could land
// on a different cluster. A host that names no port in a list that otherwise
// does gets the default port, as in libpq (pgx alone would borrow the next
// host's).
func TestConnectionString_HostListKeepsPort(t *testing.T) {
	dialed := func(t *testing.T, connStr string) string {
		t.Helper()
		cfg, err := pgconn.ParseConfig(connStr)
		if err != nil {
			t.Fatalf("pgx rejects %s: %v", connStr, err)
		}
		got := []string{net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))}
		for _, fb := range cfg.Fallbacks {
			if hp := net.JoinHostPort(fb.Host, strconv.Itoa(int(fb.Port))); hp != got[len(got)-1] {
				got = append(got, hp)
			}
		}
		return strings.Join(got, ",")
	}

	for _, tt := range []struct{ in, want string }{
		{"postgresql://u@127.0.0.1,localhost/postgres?port=5440", "127.0.0.1:5440,localhost:5440"},
		{"postgresql://u@db1,db2:5433/app", "db1:5432,db2:5433"},
		{"postgresql://u@[::1]:5432,[::2]:5433/app", "[::1]:5432,[::2]:5433"},
		{"host=db1,db2 port=6000 dbname=app", "db1:6000,db2:6000"},
		{"host=::1,::2 port=5432,5433 dbname=app", "[::1]:5432,[::2]:5433"},
		{"host=/tmp,db2 port=5433 dbname=app", "/tmp:5433,db2:5433"},
	} {
		c, err := ParseConnectionString(tt.in)
		if err != nil {
			t.Fatalf("%s: %v", tt.in, err)
		}
		if got := dialed(t, BuildConnectionString(c)); got != tt.want {
			t.Errorf("%s dials %s, want %s", tt.in, got, tt.want)
		}
	}

	fromFlags := &ConnectionConfig{Host: "db1,db2", Port: 6000, Database: "app", Username: "u", Password: "p w'd"}
	out := BuildConnectionString(fromFlags)
	if got := dialed(t, out); got != "db1:6000,db2:6000" {
		t.Errorf("-h db1,db2 -p 6000 dials %s, want db1:6000,db2:6000", got)
	}
	back, err := ParseConnectionString(out)
	if err != nil {
		t.Fatalf("rebuilt %s does not parse: %v", out, err)
	}
	if got := dialed(t, BuildConnectionString(back)); got != "db1:6000,db2:6000" || back.Password != "p w'd" || back.Database != "app" {
		t.Errorf("second round trip: dials %s, password %q, db %q", got, back.Password, back.Database)
	}
}

func TestDialTargets(t *testing.T) {
	for _, tt := range []struct {
		host string
		want string
	}{
		{"db1", "db1:6000"},
		{"::1", "[::1]:6000"},
		{"db1,db2:5433", "db1:6000,db2:5433"},
		{"[::1]:5432,/tmp", "[::1]:5432,/tmp:6000"},
	} {
		if got := DialTargets(&ConnectionConfig{Host: tt.host, Port: 6000}); got != tt.want {
			t.Errorf("DialTargets(%q) = %s, want %s", tt.host, got, tt.want)
		}
	}
}

// pgx has no hostaddr; treating it as host dialed the IP and verified the
// certificate against it instead of the name.
func TestParseConnectionString_RejectsHostaddr(t *testing.T) {
	if _, err := ParseConnectionString("host=db hostaddr=10.0.0.1 dbname=app"); err == nil {
		t.Fatal("hostaddr must be rejected")
	}
}

// The libpq keyword/value form is what psql, the PostgreSQL docs and many
// hosting dashboards hand out. It was rejected as "unrecognized".
func TestParseConnectionString_KeywordValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ConnectionConfig
	}{
		{"plain", "host=db1 port=5433 dbname=app user=me password=secret sslmode=require",
			ConnectionConfig{Host: "db1", Port: 5433, Database: "app", Username: "me", Password: "secret", SSLMode: "require"}},
		{"spaces around equals and defaults", "host = db1   dbname = app",
			ConnectionConfig{Host: "db1", Port: 5432, Database: "app"}},
		{"quoted value with space, quote and semicolon", `host=db1 password='a b\'c;d' dbname=app`,
			ConnectionConfig{Host: "db1", Port: 5432, Database: "app", Password: "a b'c;d"}},
		{"host list with one port", "host=db1,db2 port=5433 dbname=app",
			ConnectionConfig{Host: "db1:5433,db2:5433", Port: 5432, Database: "app"}},
		{"host list with a port each", "host=db1,db2 port=5432,5433 dbname=app",
			ConnectionConfig{Host: "db1:5432,db2:5433", Port: 5432, Database: "app"}},
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

// An apostrophe inside an ADO.NET value hid every ';' after it, so the whole
// string went to the keyword/value parser and its tail reached localhost as a
// startup parameter named "Password".
func TestParseConnectionString_ApostropheInADONETValue(t *testing.T) {
	got, err := ParseConnectionString("Password=it's;Host=db;Database=app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != "db" || got.Database != "app" || got.Password != "it's" {
		t.Errorf("got host=%q db=%q pass=%q", got.Host, got.Database, got.Password)
	}
	if len(got.AdditionalParams) != 0 {
		t.Errorf("unexpected pass-through params: %v", got.AdditionalParams)
	}
}

func TestParseConnectionString_KeywordValueRejectsUnknownKey(t *testing.T) {
	for _, tt := range []struct{ in, key string }{
		{"host=db bogus=1", "bogus"},
		{"host=db Password=x", "Password"},
		{"host=db dbanme=app", "dbanme"},
	} {
		_, err := ParseConnectionString(tt.in)
		if err == nil {
			t.Errorf("%q must be rejected", tt.in)
			continue
		}
		if want := fmt.Sprintf("invalid connection option %q", tt.key); !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %q does not name the key", tt.in, err)
		}
	}

	_, _, err := ResolveConnectionParams("host=db bogus=1", nil, nil, nil, nil, nil, nil, nil)
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("want ErrInvalidConfig (exit 10), got %v", err)
	}
}

func TestParseConnectionString_KeywordValuePassesLibpqKeywords(t *testing.T) {
	got, err := ParseConnectionString("host=a,b target_session_attrs=read-write options='-c search_path=app' gssencmode=disable channel_binding=require keepalives_idle=30")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"target_session_attrs": "read-write",
		"options":              "-c search_path=app",
		"gssencmode":           "disable",
		"channel_binding":      "require",
		"keepalives_idle":      "30",
	}
	for k, v := range want {
		if got.AdditionalParams[k] != v {
			t.Errorf("%s = %q, want %q", k, got.AdditionalParams[k], v)
		}
	}
	if got.Host != "a,b" {
		t.Errorf("Host = %q, want a,b", got.Host)
	}
}

func TestParseConnectionString_KeywordValueAcceptsPgxExecOptions(t *testing.T) {
	got, err := ParseConnectionString("host=db default_query_exec_mode=exec statement_cache_capacity=0 description_cache_capacity=0")
	if err != nil {
		t.Fatal(err)
	}
	built := BuildConnectionString(got)
	for k, v := range map[string]string{
		"default_query_exec_mode":    "exec",
		"statement_cache_capacity":   "0",
		"description_cache_capacity": "0",
	} {
		if got.AdditionalParams[k] != v {
			t.Errorf("%s = %q, want %q", k, got.AdditionalParams[k], v)
		}
		if want := k + "=" + v; !strings.Contains(built, want) {
			t.Errorf("built string %q lacks %s", built, want)
		}
	}
	cfg, err := pgx.ParseConfig(built)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Errorf("DefaultQueryExecMode = %v, want exec", cfg.DefaultQueryExecMode)
	}

	bad, err := ParseConnectionString("host=db default_query_exec_mode=bogus")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgx.ParseConfig(BuildConnectionString(bad)); err == nil || !strings.Contains(err.Error(), "invalid default_query_exec_mode") {
		t.Errorf("bogus exec mode: want pgx's refusal, got %v", err)
	}
}

// url.Parse errors quote the raw string, and some quote a slice of the
// password ("invalid port \":p\"" for p#ss), so a URI with userinfo that fails
// to parse is reported without either.
func TestParseConnectionString_UnparsableURINeverEchoesThePassword(t *testing.T) {
	for _, pw := range []string{"p ss", "p#ss", "p%zz", "top secret#1"} {
		_, err := ParseConnectionString("postgresql://u:" + pw + "@h/db")
		if err == nil {
			t.Fatalf("%q: want a parse error", pw)
		}
		for _, leak := range []string{pw, strings.Fields(pw)[0][1:], ":p"} {
			if leak != "" && strings.Contains(err.Error(), leak) {
				t.Errorf("%q: the error shows %q: %v", pw, leak, err)
			}
		}
	}
	if _, err := ParseConnectionString("postgresql://h:port/db"); err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("without userinfo the parse error keeps its detail, got %v", err)
	}
}

// What the user wrote reaches pgx unchanged: parse, rebuild, then
// pgx.ParseConfig, as a deploy does.
func TestConnectionString_RoundTripKeepsWhatWasWritten(t *testing.T) {
	t.Setenv("PGAPPNAME", "")
	t.Setenv("PGOPTIONS", "")
	reach := func(t *testing.T, connStr string) *pgx.ConnConfig {
		t.Helper()
		config, err := ParseConnectionString(connStr)
		if err != nil {
			t.Fatalf("parse %q: %v", connStr, err)
		}
		built, err := pgx.ParseConfig(BuildConnectionString(config))
		if err != nil {
			t.Fatalf("pgx parse of %q: %v", BuildConnectionString(config), err)
		}
		return built
	}
	for _, tc := range []struct {
		name, connStr, param, want string
	}{
		{"options with a space, URI", "postgresql://u@h/d?options=-c%20search_path%3Dfoo", "options", "-c search_path=foo"},
		{"options with a space, keyword", "host=h user=u dbname=d options='-c search_path=foo'", "options", "-c search_path=foo"},
		{"application_name with a space", "postgresql://u@h/d?application_name=my%20app", "application_name", "my app"},
		{"a literal plus stays a plus", "postgresql://u@h/d?application_name=a+b", "application_name", "a+b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reach(t, tc.connStr).RuntimeParams[tc.param]; got != tc.want {
				t.Errorf("%s = %q, want %q", tc.param, got, tc.want)
			}
		})
	}

	t.Run("a query dbname, user and password are the target, not passed on to override it", func(t *testing.T) {
		config, err := ParseConnectionString("postgresql://u:p@h/postgres?dbname=vgo_b&user=q&password=s")
		if err != nil {
			t.Fatal(err)
		}
		if config.Database != "vgo_b" || config.Username != "q" || config.Password != "s" || len(config.AdditionalParams) != 0 {
			t.Fatalf("got %+v", config)
		}
		config.Database = "vgo_a"
		built, err := pgx.ParseConfig(BuildConnectionString(config))
		if err != nil {
			t.Fatal(err)
		}
		if built.Database != "vgo_a" {
			t.Errorf("-d vgo_a connected to %q", built.Database)
		}
	})

	t.Run("an ADO.NET dbname is the database", func(t *testing.T) {
		config, err := ParseConnectionString("Host=h;Database=a;dbname=b")
		if err != nil {
			t.Fatal(err)
		}
		if config.Database != "b" || len(config.AdditionalParams) != 0 {
			t.Errorf("got %+v", config)
		}
	})

	t.Run("a password without a user is kept", func(t *testing.T) {
		built := reach(t, "host=h password=pw dbname=d")
		if built.Password != "pw" || built.Database != "d" {
			t.Errorf("password %q, database %q", built.Password, built.Database)
		}
	})
}

func TestGoogleConnector_NamesAreValuesNotKeywords(t *testing.T) {
	c := NewGoogleCloudSQLConnector(&ConnectionConfig{Username: "svc@p.iam", Database: "staging dbname=production"}, "p:r:i", Output{})
	cfg, err := c.poolConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "staging dbname=production" || cfg.ConnConfig.User != "svc@p.iam" || cfg.ConnConfig.Host != "p:r:i" {
		t.Errorf("got host %q user %q database %q", cfg.ConnConfig.Host, cfg.ConnConfig.User, cfg.ConnConfig.Database)
	}
}

func TestParseConnectionString_ADONETAsNpgsqlReadsIt(t *testing.T) {
	c, err := ParseConnectionString("Host=127.0.0.1:5433;Port=5999;Database=d;Username=u")
	if err != nil || c.Host != "127.0.0.1" || c.Port != 5433 {
		t.Fatalf("a single Host=h:port must split, its port winning: %+v %v", c, err)
	}
	if got := BuildConnectionString(c); !strings.Contains(got, "127.0.0.1:5433") {
		t.Errorf("built %q, want host 127.0.0.1 port 5433", got)
	}
	for in, want := range map[string]string{"Require": "require", "VerifyFull": "verify-full", "VerifyCA": "verify-ca",
		"Disable": "disable", "verify-full": "verify-full"} {
		c, err := ParseConnectionString("Host=h;Database=d;SSL Mode=" + in)
		if err != nil || c.SSLMode != want {
			t.Errorf("SSL Mode=%s: got %q, %v; want %q", in, c.SSLMode, err, want)
		}
	}
	for _, key := range []string{"Pooling", "Trust Server Certificate"} {
		_, err := ParseConnectionString("Host=h;Database=d;" + key + "=true")
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s must be refused by name, got %v", key, err)
		}
	}
}
