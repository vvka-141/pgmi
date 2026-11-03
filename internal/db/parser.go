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
	config, err := parseConnectionStringRaw(connStr)
	if err != nil {
		return nil, err
	}
	applyStaticDefaults(config)
	return config, nil
}

// applyStaticDefaults fills what neither the string nor the environment gave.
func applyStaticDefaults(config *pgmi.ConnectionConfig) {
	if config.Host == "" {
		config.Host = "localhost"
	}
	if config.Port == 0 {
		config.Port = 5432
	}
	if config.Database == "" {
		config.Database = pgmi.DefaultMaintenanceDB
	}
}

// parseConnectionStringRaw leaves host, port and dbname empty when the string
// omits them, so the caller can take them from the environment first.
func parseConnectionStringRaw(connStr string) (*pgmi.ConnectionConfig, error) {
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
var errServiceFile = fmt.Errorf("service files (service=, PGSERVICE) are not supported: give host, port and dbname in the connection string, flags or PGHOST/PGPORT/PGDATABASE")

func parsePostgreSQLURI(connStr string) (*pgmi.ConnectionConfig, error) {
	connStr, hostList := liftHostList(connStr)
	u, err := url.Parse(connStr)
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL URI: %w", err)
	}

	config := &pgmi.ConnectionConfig{
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
		case "service":
			return nil, errServiceFile
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

// BuildConnectionString converts a ConnectionConfig back to a connection
// string pgx accepts: a URI for one host, the keyword/value form for a host
// list, since a URI cannot carry a port for hosts that do not name one.
func BuildConnectionString(config *pgmi.ConnectionConfig) string {
	if strings.Contains(config.Host, ",") {
		return buildKeywordValue(config)
	}
	u := &url.URL{
		Scheme: "postgresql",
		Path:   "/" + config.Database,
	}
	query := url.Values{}
	if strings.HasPrefix(config.Host, "/") {
		query.Set("host", config.Host)
		query.Set("port", strconv.Itoa(config.Port))
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

func buildKeywordValue(config *pgmi.ConnectionConfig) string {
	members := strings.Split(config.Host, ",")
	hosts := make([]string, len(members))
	ports := make([]string, len(members))
	for i, m := range members {
		hosts[i], ports[i] = splitHostMember(m, config.Port)
	}
	pairs := [][2]string{
		{"host", strings.Join(hosts, ",")},
		{"port", strings.Join(ports, ",")},
		{"dbname", config.Database},
		{"user", config.Username},
		{"password", config.Password},
		{"sslmode", config.SSLMode},
		{"sslcert", config.SSLCert},
		{"sslkey", config.SSLKey},
		{"sslrootcert", config.SSLRootCert},
		{"sslpassword", config.SSLPassword},
		{"application_name", config.AppName},
	}
	if config.ConnectTimeout > 0 {
		pairs = append(pairs, [2]string{"connect_timeout", strconv.Itoa(int(config.ConnectTimeout.Seconds()))})
	}
	for key, value := range config.AdditionalParams {
		pairs = append(pairs, [2]string{key, value})
	}
	var b strings.Builder
	for _, kv := range pairs {
		if kv[1] == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(kv[0])
		b.WriteString("='")
		b.WriteString(strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(kv[1]))
		b.WriteByte('\'')
	}
	return b.String()
}

// DialTargets lists every host:port pgmi will try, for messages.
func DialTargets(config *pgmi.ConnectionConfig) string {
	members := strings.Split(config.Host, ",")
	for i, m := range members {
		members[i] = net.JoinHostPort(splitHostMember(m, config.Port))
	}
	return strings.Join(members, ",")
}

// splitHostMember splits one entry of a host list into host and port, using
// defaultPort when the entry names none: db1, db1:5433, [::1]:5433, ::1,
// /var/run/postgresql, /var/run/postgresql:5433.
func splitHostMember(member string, defaultPort int) (string, string) {
	port := strconv.Itoa(defaultPort)
	if strings.HasPrefix(member, "[") {
		if end := strings.Index(member, "]"); end > 0 {
			if p, ok := strings.CutPrefix(member[end+1:], ":"); ok && p != "" {
				port = p
			}
			return member[1:end], port
		}
	}
	if strings.Count(member, ":") > 1 && !strings.HasPrefix(member, "/") {
		return member, port
	}
	if i := strings.LastIndex(member, ":"); i >= 0 {
		if _, err := strconv.Atoi(member[i+1:]); err == nil {
			return member[:i], member[i+1:]
		}
	}
	return member, port
}
