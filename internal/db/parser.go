package db

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// ParseConnectionString parses a PostgreSQL connection string and returns a
// ConnectionConfig.
//
// Supported formats:
//   - PostgreSQL URI: postgresql://user:pass@localhost:5432/dbname?sslmode=disable
//   - libpq keyword/value: host=localhost port=5432 dbname=dbname user=user
//   - ADO.NET: Host=localhost;Port=5432;Database=dbname;Username=user;Password=pass
func ParseConnectionString(connStr string) (*pgmi.ConnectionConfig, error) {
	if connStr == "" {
		return nil, fmt.Errorf("connection string is empty")
	}

	// Try PostgreSQL URI format first
	if strings.HasPrefix(connStr, "postgresql://") || strings.HasPrefix(connStr, "postgres://") {
		return parsePostgreSQLURI(connStr)
	}

	if strings.Contains(connStr, "=") && hasUnquotedSemicolon(connStr) {
		return parseADONET(connStr)
	}
	if strings.Contains(connStr, "=") {
		return parseKeywordValue(connStr)
	}

	return nil, fmt.Errorf("unrecognized connection string format: use postgresql://user@host:5432/dbname, host=... dbname=..., or Host=...;Database=...")
}

// parsePostgreSQLURI parses a PostgreSQL URI format connection string.
// Format: postgresql://[user[:password]@][host][:port][/dbname][?param1=value1&...]
func parsePostgreSQLURI(connStr string) (*pgmi.ConnectionConfig, error) {
	connStr, hostList := liftHostList(connStr)
	u, err := url.Parse(connStr)
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL URI: %w", err)
	}

	config := &pgmi.ConnectionConfig{
		Host:             "localhost",
		Port:             5432,
		Database:         "postgres",
		SSLMode:          "",
		AuthMethod:       pgmi.AuthMethodStandard,
		AdditionalParams: make(map[string]string),
	}

	// Parse host and port. A multi-host list (h1:5432,h2:5433) is kept whole
	// and passed through; taking it apart lost every host but one.
	if hostList != "" {
		config.Host = hostList
	} else if u.Hostname() != "" {
		config.Host = u.Hostname()
	}
	if hostList == "" && u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil {
			return nil, fmt.Errorf("invalid port: %w", err)
		}
		if err := validatePort(port); err != nil {
			return nil, err
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
		case "host":
			config.Host = value
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid port: %w", err)
			}
			if err := validatePort(port); err != nil {
				return nil, err
			}
			config.Port = port
		case "sslmode":
			config.SSLMode = value
		case "sslcert":
			config.SSLCert = value
		case "sslkey":
			config.SSLKey = value
		case "sslrootcert":
			config.SSLRootCert = value
		case "sslpassword":
			config.SSLPassword = value
		case "application_name", "applicationname":
			config.AppName = value
		case "connect_timeout", "connecttimeout":
			timeout, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid connect_timeout value %q: %w", value, err)
			}
			config.ConnectTimeout = time.Duration(timeout) * time.Second
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
		SSLMode:          "",
		AuthMethod:       pgmi.AuthMethodStandard,
		AdditionalParams: make(map[string]string),
	}

	for _, kv := range splitADONETPairs(connStr) {
		key := kv.key
		value := kv.value

		switch strings.ToLower(key) {
		case "host", "server":
			config.Host = value
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid port in ADO.NET string: %w", err)
			}
			if err := validatePort(port); err != nil {
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
		case "sslcert", "ssl cert":
			config.SSLCert = value
		case "sslkey", "ssl key":
			config.SSLKey = value
		case "sslrootcert", "ssl root cert":
			config.SSLRootCert = value
		case "sslpassword", "ssl password":
			config.SSLPassword = value
		case "application name", "applicationname":
			config.AppName = value
		case "timeout", "connect timeout", "connecttimeout":
			timeout, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid timeout value %q: %w", value, err)
			}
			config.ConnectTimeout = time.Duration(timeout) * time.Second
		default:
			config.AdditionalParams[key] = value
		}
	}

	return config, nil
}

type adoPair struct {
	key   string
	value string
}

// splitADONETPairs splits an ADO.NET connection string into key/value pairs,
// honoring ADO.NET quoting: a value may be wrapped in single or double quotes
// to contain ';' or '=', and a doubled quote inside is a literal quote. Without
// this, a password containing ';' would be truncated by a naive split.
func splitADONETPairs(s string) []adoPair {
	var pairs []adoPair
	i, n := 0, len(s)
	for i < n {
		for i < n && (s[i] == ' ' || s[i] == ';') { // skip separators/spaces
			i++
		}
		if i >= n {
			break
		}

		keyStart := i
		for i < n && s[i] != '=' && s[i] != ';' {
			i++
		}
		if i >= n || s[i] != '=' { // malformed token without '='; skip it
			for i < n && s[i] != ';' {
				i++
			}
			continue
		}
		key := strings.TrimSpace(s[keyStart:i])
		i++ // consume '='

		for i < n && s[i] == ' ' { // skip spaces before value
			i++
		}

		var value string
		if i < n && (s[i] == '\'' || s[i] == '"') {
			quote := s[i]
			i++
			var sb strings.Builder
			for i < n {
				if s[i] == quote {
					if i+1 < n && s[i+1] == quote { // doubled quote = literal
						sb.WriteByte(quote)
						i += 2
						continue
					}
					i++ // closing quote
					break
				}
				sb.WriteByte(s[i])
				i++
			}
			value = sb.String()
			for i < n && s[i] != ';' { // discard trailing junk up to ';'
				i++
			}
		} else {
			valStart := i
			for i < n && s[i] != ';' {
				i++
			}
			value = strings.TrimSpace(s[valStart:i])
		}

		if key != "" {
			pairs = append(pairs, adoPair{key: key, value: value})
		}
	}
	return pairs
}

// BuildConnectionString converts a ConnectionConfig back to a PostgreSQL URI format.
// This is useful for creating connection strings for pgx.
func BuildConnectionString(config *pgmi.ConnectionConfig) string {
	u := &url.URL{
		Scheme: "postgresql",
		Path:   "/" + config.Database,
	}
	query := url.Values{}
	if strings.HasPrefix(config.Host, "/") {
		query.Set("host", config.Host)
		query.Set("port", strconv.Itoa(config.Port))
	} else if strings.Contains(config.Host, ",") {
		u.Host = config.Host
	} else {
		u.Host = net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	}

	if config.Username != "" {
		if config.Password != "" {
			u.User = url.UserPassword(config.Username, config.Password)
		} else {
			u.User = url.User(config.Username)
		}
	}

	if config.SSLMode != "" {
		query.Set("sslmode", config.SSLMode)
	}
	if config.SSLCert != "" {
		query.Set("sslcert", config.SSLCert)
	}
	if config.SSLKey != "" {
		query.Set("sslkey", config.SSLKey)
	}
	if config.SSLRootCert != "" {
		query.Set("sslrootcert", config.SSLRootCert)
	}
	if config.SSLPassword != "" {
		query.Set("sslpassword", config.SSLPassword)
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

// liftHostList swaps a comma-separated host list out of a URI's authority for
// a placeholder, because url.Parse rejects one (with IPv6 members especially),
// and returns the list so the caller can keep it verbatim.
func liftHostList(connStr string) (string, string) {
	schemeEnd := strings.Index(connStr, "://")
	if schemeEnd < 0 {
		return connStr, ""
	}
	start := schemeEnd + 3
	end := len(connStr)
	if i := strings.IndexAny(connStr[start:], "/?"); i >= 0 {
		end = start + i
	}
	if at := strings.LastIndex(connStr[start:end], "@"); at >= 0 {
		start += at + 1
	}
	hosts := connStr[start:end]
	if !strings.Contains(hosts, ",") {
		return connStr, ""
	}
	return connStr[:start] + "multihost.invalid" + connStr[end:], hosts
}
