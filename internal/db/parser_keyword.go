package db

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// parseKeywordValue parses the libpq keyword/value form psql and the
// PostgreSQL docs use: host=db1 port=5432 dbname=app user=me.
// See https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING-KEYWORD-VALUE
func parseKeywordValue(connStr string) (*ConnectionConfig, error) {
	pairs, err := splitKeywordValuePairs(connStr)
	if err != nil {
		return nil, err
	}

	config := &ConnectionConfig{
		AuthMethod:       pgmi.AuthMethodStandard,
		AdditionalParams: make(map[string]string),
	}

	var hosts, ports []string
	for _, kv := range pairs {
		switch kv.key {
		case "host":
			hosts = strings.Split(kv.value, ",")
		case "hostaddr":
			return nil, fmt.Errorf("hostaddr is not supported: put the address in host")
		case "port":
			ports = strings.Split(kv.value, ",")
		case "dbname":
			config.Database = kv.value
		case "user":
			config.Username = kv.value
		case "password":
			config.Password = kv.value
		case "sslmode":
			config.SSLMode = kv.value
		case "sslcert":
			config.SSLCert = kv.value
		case "sslkey":
			config.SSLKey = kv.value
		case "sslrootcert":
			config.SSLRootCert = kv.value
		case "sslpassword":
			config.SSLPassword = kv.value
		case "service":
			return nil, errServiceFile
		case "application_name":
			config.AppName = kv.value
		case "connect_timeout":
			seconds, err := strconv.Atoi(kv.value)
			if err != nil {
				return nil, fmt.Errorf("invalid connect_timeout %q: %w", kv.value, err)
			}
			config.ConnectTimeout = time.Duration(seconds) * time.Second
		default:
			if !libpqKeywords[kv.key] && !pgxKeywords[kv.key] {
				return nil, fmt.Errorf("invalid connection option %q", kv.key)
			}
			config.AdditionalParams[kv.key] = kv.value
		}
	}

	switch {
	case len(hosts) > 1 || len(ports) > 1:
		list, err := joinHostList(hosts, ports)
		if err != nil {
			return nil, err
		}
		config.Host = list
	default:
		if len(hosts) == 1 && hosts[0] != "" {
			config.Host = hosts[0]
		}
		if len(ports) == 1 && ports[0] != "" {
			port, err := strconv.Atoi(ports[0])
			if err != nil {
				return nil, fmt.Errorf("invalid port %q: %w", ports[0], err)
			}
			if err := validatePort(port); err != nil {
				return nil, err
			}
			config.Port = port
		}
	}
	return config, nil
}

// joinHostList turns libpq's parallel host and port lists into pgmi's host
// list (h1:5432,[::1]:5433). One port applies to every host.
func joinHostList(hosts, ports []string) (string, error) {
	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}
	if len(ports) > 1 && len(ports) != len(hosts) {
		return "", fmt.Errorf("%d hosts but %d ports: give one port, or one per host", len(hosts), len(ports))
	}
	parts := make([]string, len(hosts))
	for i, h := range hosts {
		port := ""
		switch {
		case len(ports) == 1:
			port = ports[0]
		case len(ports) > 1:
			port = ports[i]
		}
		if port == "" {
			parts[i] = h
		} else {
			parts[i] = net.JoinHostPort(h, port)
		}
	}
	return strings.Join(parts, ","), nil
}

// libpqKeywords is libpq's connection keyword set (PostgreSQL 18). Like
// libpq, the keyword/value form refuses anything else: an unknown key would
// otherwise reach the server as a startup parameter.
// See https://www.postgresql.org/docs/18/libpq-connect.html#LIBPQ-PARAMKEYWORDS
var libpqKeywords = map[string]bool{
	"host": true, "hostaddr": true, "port": true, "dbname": true, "user": true,
	"password": true, "passfile": true, "require_auth": true, "channel_binding": true,
	"connect_timeout": true, "client_encoding": true, "options": true,
	"application_name": true, "fallback_application_name": true,
	"keepalives": true, "keepalives_idle": true, "keepalives_interval": true,
	"keepalives_count": true, "tcp_user_timeout": true, "replication": true,
	"gssencmode": true, "sslmode": true, "requiressl": true, "sslnegotiation": true,
	"sslcompression": true, "sslcert": true, "sslkey": true, "sslkeylogfile": true,
	"sslpassword": true, "sslcertmode": true, "sslrootcert": true, "sslcrl": true,
	"sslcrldir": true, "sslsni": true, "requirepeer": true,
	"ssl_min_protocol_version": true, "ssl_max_protocol_version": true,
	"min_protocol_version": true, "max_protocol_version": true,
	"krbsrvname": true, "gsslib": true, "gssdelegation": true,
	"scram_client_key": true, "scram_server_key": true, "service": true,
	"target_session_attrs": true, "load_balance_hosts": true,
	"oauth_issuer": true, "oauth_client_id": true, "oauth_client_secret": true,
	"oauth_scope": true,
}

// pgxKeywords are the pgx options pgmi passes through, as the URI form does:
// a single-session server (the PGlite socket) needs default_query_exec_mode=exec
// so a second connection does not collide with the first's prepared statements.
var pgxKeywords = map[string]bool{
	"default_query_exec_mode": true, "statement_cache_capacity": true,
	"description_cache_capacity": true,
}

type keywordPair struct {
	key   string
	value string
}

// splitKeywordValuePairs follows libpq: pairs are separated by whitespace,
// spaces around '=' are allowed, a value may be single-quoted, and a
// backslash escapes the next character in either form.
func splitKeywordValuePairs(s string) ([]keywordPair, error) {
	var pairs []keywordPair
	i, n := 0, len(s)
	skipSpace := func() {
		for i < n && isSpace(s[i]) {
			i++
		}
	}
	for {
		skipSpace()
		if i >= n {
			return pairs, nil
		}
		start := i
		for i < n && s[i] != '=' && !isSpace(s[i]) {
			i++
		}
		key := s[start:i]
		skipSpace()
		if i >= n || s[i] != '=' {
			return nil, fmt.Errorf("missing \"=\" after %q in connection string", key)
		}
		i++
		skipSpace()

		var b strings.Builder
		if i < n && s[i] == '\'' {
			i++
			closed := false
			for i < n {
				c := s[i]
				if c == '\\' && i+1 < n {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '\'' {
					i++
					closed = true
					break
				}
				b.WriteByte(c)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted value for %q in connection string", key)
			}
		} else {
			for i < n && !isSpace(s[i]) {
				if s[i] == '\\' && i+1 < n {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				b.WriteByte(s[i])
				i++
			}
		}
		pairs = append(pairs, keywordPair{key: key, value: b.String()})
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// hasUnquotedSemicolon reports whether s contains ';' outside libpq single
// quotes, which is what separates the ADO.NET form from the keyword/value form
// when a quoted password happens to contain ';'. As in libpq, a quote opens
// only at the start of a value: the ' in Password=it's is a plain character.
func hasUnquotedSemicolon(s string) bool {
	quoted, valueStart := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quoted {
			switch c {
			case '\\':
				i++
			case '\'':
				quoted = false
			}
			continue
		}
		switch {
		case c == ';':
			return true
		case c == '=':
			valueStart = true
		case c == '\'' && valueStart:
			quoted = true
			valueStart = false
		case isSpace(c):
		default:
			valueStart = false
			if c == '\\' {
				i++
			}
		}
	}
	return false
}
