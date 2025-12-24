package db

import (
	"testing"
)

// FuzzParseConnectionString fuzzes the connection string parser to find edge cases
func FuzzParseConnectionString(f *testing.F) {
	// Seed corpus with known valid connection strings
	f.Add("postgresql://user:pass@localhost:5432/db")
	f.Add("postgresql://user@localhost/db")
	f.Add("postgres://localhost:5432/db")
	f.Add("Host=localhost;Port=5432;Database=db;Username=user;Password=pass")
	f.Add("Host=localhost;Database=db")
	f.Add("Server=localhost;Port=5432;Database=db;User ID=user;Password=pass")
	f.Add("postgresql://user:p@ss%20w0rd@localhost:5432/db?sslmode=require")
	f.Add("postgresql://user@localhost:5432/db?application_name=pgmi")

	// Seed with edge cases
	f.Add("")
	f.Add("not-a-connection-string")
	f.Add("postgresql://")
	f.Add("Host=")
	f.Add(";;;")
	f.Add("Host=localhost;Port=abc;Database=db")

	f.Fuzz(func(t *testing.T, connStr string) {
		// The parser should never panic, regardless of input
		_, err := ParseConnectionString(connStr)

		// We don't care if it errors (invalid input is expected),
		// but it must not panic
		_ = err
	})
}

// FuzzBuildConnectionString fuzzes the connection string builder
func FuzzBuildConnectionString(f *testing.F) {
	f.Add("localhost", int32(5432), "testdb", "user", "pass", "pgmi")
	f.Add("", int32(0), "", "", "", "")
	f.Add("host", int32(-1), "db", "u", "p", "app")
	f.Add("::1", int32(5432), "db", "user", "pass", "app")
	f.Add("localhost", int32(65535), "db", "user", "pass", "app")

	f.Fuzz(func(t *testing.T, host string, port int32, database, username, password, appName string) {
		// Create a connection config
		config, err := ParseConnectionString("postgresql://localhost:5432/db")
		if err != nil {
			return
		}

		// Override with fuzz inputs (convert int32 to int for Port field)
		config.Host = host
		config.Port = int(port)
		config.Database = database
		config.Username = username
		config.Password = password
		config.AppName = appName

		// Building should never panic
		result := BuildConnectionString(config)

		// Result should be a non-empty string if we have required fields
		if host != "" && database != "" {
			if result == "" {
				t.Errorf("BuildConnectionString returned empty string for valid inputs")
			}
		}
	})
}
