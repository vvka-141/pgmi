package db

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// ParseConnectionString parses a PostgreSQL connection string in either
// PostgreSQL URI format or ADO.NET format and returns a ConnectionConfig.
//
// Supported formats:
//   - PostgreSQL URI: postgresql://user:pass@localhost:5432/dbname?sslmode=disable
//   - ADO.NET: Host=localhost;Port=5432;Database=dbname;Username=user;Password=pass
func ParseConnectionString(connStr string) (*pgmi.ConnectionConfig, error) {
	if connStr == "" {
		return nil, fmt.Errorf("connection string is empty")
	}

	// Try PostgreSQL URI format first
	if strings.HasPrefix(connStr, "postgresql://") || strings.HasPrefix(connStr, "postgres://") {
		return parsePostgreSQLURI(connStr)
	}

	// Try ADO.NET format
	if strings.Contains(connStr, "=") && strings.Contains(connStr, ";") {
		return parseADONET(connStr)
	}

	return nil, fmt.Errorf("unrecognized connection string format")
}

// parsePostgreSQLURI parses a PostgreSQL URI format connection string.
// Format: postgresql://[user[:password]@][host][:port][/dbname][?param1=value1&...]
func parsePostgreSQLURI(connStr string) (*pgmi.ConnectionConfig, error) {
	u, err := url.Parse(connStr)
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL URI: %w", err)
	}

	config := &pgmi.ConnectionConfig{
		Host:             "localhost",
		Port:             5432,
		Database:         "postgres",
		SSLMode:          "prefer",
		AuthMethod:       pgmi.AuthMethodStandard,
		AdditionalParams: make(map[string]string),
	}

	// Parse host and port
	if u.Hostname() != "" {
		config.Host = u.Hostname()
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			return nil, fmt.Errorf("invalid port: %w", err)
		}
		config.Port = port
	}

	// Parse username and password
	if u.User != nil {
		config.Username = u.User.Username()
		if pass, ok := u.User.Password(); ok {
			config.Password = pass
		}
	}

	// Parse database name
	if len(u.Path) > 1 {
		config.Database = strings.TrimPrefix(u.Path, "/")
	}

	// Parse query parameters
	query := u.Query()
	for key, values := range query {
		if len(values) == 0 {
			continue
		}
		value := values[0]

		switch strings.ToLower(key) {
		case "sslmode":
			config.SSLMode = value
		case "application_name", "applicationname":
			config.AppName = value
		case "connect_timeout", "connecttimeout":
			timeout, err := strconv.Atoi(value)
			if err == nil {
				config.ConnectTimeout = time.Duration(timeout) * time.Second
			}
		default:
			config.AdditionalParams[key] = value
		}
	}

	return config, nil
}

// parseADONET parses an ADO.NET format connection string.
// Format: Host=localhost;Port=5432;Database=dbname;Username=user;Password=pass;...
func parseADONET(connStr string) (*pgmi.ConnectionConfig, error) {
	config := &pgmi.ConnectionConfig{
		Host:             "localhost",
		Port:             5432,
		Database:         "postgres",
		SSLMode:          "prefer",
		AuthMethod:       pgmi.AuthMethodStandard,
		AdditionalParams: make(map[string]string),
	}

	parts := strings.Split(connStr, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])

		switch strings.ToLower(key) {
		case "host", "server":
			config.Host = value
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid port in ADO.NET string: %w", err)
			}
			config.Port = port
		case "database", "initial catalog":
			config.Database = value
		case "username", "user id", "uid":
			config.Username = value
		case "password", "pwd":
			config.Password = value
		case "sslmode", "ssl mode":
			config.SSLMode = value
		case "application name", "applicationname":
			config.AppName = value
		case "timeout", "connect timeout", "connecttimeout":
			timeout, err := strconv.Atoi(value)
			if err == nil {
				config.ConnectTimeout = time.Duration(timeout) * time.Second
			}
		default:
			config.AdditionalParams[key] = value
		}
	}

	return config, nil
}

// BuildConnectionString converts a ConnectionConfig back to a PostgreSQL URI format.
// This is useful for creating connection strings for pgx.
func BuildConnectionString(config *pgmi.ConnectionConfig) string {
	u := &url.URL{
		Scheme: "postgresql",
		Host:   fmt.Sprintf("%s:%d", config.Host, config.Port),
		Path:   "/" + config.Database,
	}

	if config.Username != "" {
		if config.Password != "" {
			u.User = url.UserPassword(config.Username, config.Password)
		} else {
			u.User = url.User(config.Username)
		}
	}

	query := url.Values{}
	if config.SSLMode != "" {
		query.Set("sslmode", config.SSLMode)
	}
	if config.AppName != "" {
		query.Set("application_name", config.AppName)
	}
	if config.ConnectTimeout > 0 {
		query.Set("connect_timeout", strconv.Itoa(int(config.ConnectTimeout.Seconds())))
	}

	for key, value := range config.AdditionalParams {
		query.Set(key, value)
	}

	u.RawQuery = query.Encode()
	return u.String()
}
