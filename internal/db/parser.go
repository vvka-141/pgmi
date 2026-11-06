package db

import (
	"errors"
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
func ParseConnectionString(connStr string) (*ConnectionConfig, error) {
	config, err := parseConnectionStringRaw(connStr)
	if err != nil {
		return nil, err
	}
	applyStaticDefaults(config)
	return config, nil
}

// applyStaticDefaults fills what neither the string nor the environment gave.
func applyStaticDefaults(config *ConnectionConfig) {
	if config.Host == "" {
		config.Host = "localhost"
	}
	if config.Port == 0 {
		config.Port = 5432
	}
	if config.Database == "" {
		config.Database = DefaultMaintenanceDB
	}
}

// parseConnectionStringRaw leaves host, port and dbname empty when the string
// omits them, so the caller can take them from the environment first.
func parseConnectionStringRaw(connStr string) (*ConnectionConfig, error) {
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

func parsePostgreSQLURI(connStr string) (*ConnectionConfig, error) {
	connStr, hostList := liftHostList(connStr)
	u, err := url.Parse(connStr)
	if err != nil {
		if _, rest, ok := strings.Cut(connStr, "://"); ok && strings.Contains(rest, "@") {
			return nil, errors.New("invalid PostgreSQL URI (not shown, it may hold a password): percent-encode a space, #, %, / or @ in the user or password")
		}
		return nil, fmt.Errorf("invalid PostgreSQL URI: %w", err)
	}

	config := &ConnectionConfig{
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

	query, err := uriQuery(u.RawQuery)
	if err != nil {
		return nil, err
	}
	for _, kv := range query {
		key, value := kv.key, kv.value
		switch strings.ToLower(key) {
		case "host":
			config.Host = value
		case "dbname":
			config.Database = value
		case "user":
			config.Username = value
		case "password":
			config.Password = value
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

// uriQuery reads a URI's query as libpq and pgx do: a '+' is a plus, not a
// space (url.ParseQuery would turn it into one). The first value of a
// repeated key wins, as before.
func uriQuery(raw string) ([]adoPair, error) {
	var pairs []adoPair
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(part, "=")
		key, err := url.PathUnescape(rawKey)
		if err != nil {
			return nil, fmt.Errorf("invalid PostgreSQL URI query: %w", err)
		}
		value, err := url.PathUnescape(rawValue)
		if err != nil {
			return nil, fmt.Errorf("invalid PostgreSQL URI query parameter %q: %w", key, err)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		pairs = append(pairs, adoPair{key: key, value: value})
	}
	return pairs, nil
}

// parseADONET parses an ADO.NET format connection string.
// Format: Host=localhost;Port=5432;Database=dbname;Username=user;Password=pass;...
func parseADONET(connStr string) (*ConnectionConfig, error) {
	config := &ConnectionConfig{
		SSLMode:          "",
		AuthMethod:       pgmi.AuthMethodStandard,
		AdditionalParams: make(map[string]string),
	}

	hostPort := ""
	for _, kv := range splitADONETPairs(connStr) {
		key := kv.key
		value := kv.value

		switch strings.ToLower(key) {
		case "host", "server":
			config.Host = value
			if !strings.Contains(value, ",") {
				if host, port := splitHostMember(value, 0); port != "0" {
					config.Host, hostPort = host, port
				}
			}
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid port in ADO.NET string: %w", err)
			}
			if err := validatePort(port); err != nil {
				return nil, fmt.Errorf("invalid port in ADO.NET string: %w", err)
			}
			config.Port = port
		case "database", "initial catalog", "dbname":
			config.Database = value
		case "username", "user id", "uid", "user":
			config.Username = value
		case "password", "pwd":
			config.Password = value
		case "sslmode", "ssl mode":
			config.SSLMode = npgsqlSSLModes[strings.ToLower(value)]
			if config.SSLMode == "" {
				config.SSLMode = value
			}
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
			if !libpqKeywords[key] {
				return nil, fmt.Errorf("invalid connection option %q: pgmi does not use Npgsql client options; remove it", key)
			}
			config.AdditionalParams[key] = value
		}
	}
	// Npgsql: a port in Host overrides Port=, wherever Port= stands.
	if hostPort != "" {
		port, err := strconv.Atoi(hostPort)
		if err != nil || validatePort(port) != nil {
			return nil, fmt.Errorf("invalid port %q in ADO.NET Host", hostPort)
		}
		config.Port = port
	}

	return config, nil
}

// npgsqlSSLModes maps Npgsql's SslMode names, in any case, to libpq's sslmode.
var npgsqlSSLModes = map[string]string{
	"disable": "disable", "allow": "allow", "prefer": "prefer", "require": "require",
	"verifyca": "verify-ca", "verify-ca": "verify-ca", "verifyfull": "verify-full", "verify-full": "verify-full",
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
func BuildConnectionString(config *ConnectionConfig) string {
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

	switch {
	case config.Username != "" && config.Password != "":
		u.User = url.UserPassword(config.Username, config.Password)
	case config.Username != "":
		u.User = url.User(config.Username)
	case config.Password != "":
		// url.UserPassword("", pw) would write an empty user before it.
		query.Set("password", config.Password)
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

	// pgx decodes the query as libpq does, where '+' is a plus: a space
	// must be %20, never Encode's '+'.
	u.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
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

func buildKeywordValue(config *ConnectionConfig) string {
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
func DialTargets(config *ConnectionConfig) string {
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

// DefaultMaintenanceDB is the database pgmi connects to for maintenance
// operations when nothing names one.
const DefaultMaintenanceDB = "postgres"
