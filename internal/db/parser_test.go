package db

import (
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestParseConnectionString_PostgreSQLURI(t *testing.T) {
	tests := []struct {
		name     string
		connStr  string
		want     *pgmi.ConnectionConfig
		wantErr  bool
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
		name     string
		connStr  string
		want     *pgmi.ConnectionConfig
		wantErr  bool
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
}
