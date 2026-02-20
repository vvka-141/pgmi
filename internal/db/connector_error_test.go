package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestWrapConnectionError(t *testing.T) {
	tests := []struct {
		name         string
		errMsg       string
		host         string
		port         int
		database     string
		wantContains string
	}{
		{
			name:         "connection refused",
			errMsg:       "dial tcp 127.0.0.1:5432: connection refused",
			host:         "127.0.0.1",
			port:         5432,
			database:     "mydb",
			wantContains: "connection refused to 127.0.0.1:5432",
		},
		{
			name:         "actively refused (Windows)",
			errMsg:       "dial tcp 127.0.0.1:5432: connectex: No connection could be made because the target machine actively refused it",
			host:         "127.0.0.1",
			port:         5432,
			database:     "mydb",
			wantContains: "connection refused to 127.0.0.1:5432",
		},
		{
			name:         "no such host",
			errMsg:       "dial tcp: lookup badhost.example.com: no such host",
			host:         "badhost.example.com",
			port:         5432,
			database:     "mydb",
			wantContains: `cannot resolve host "badhost.example.com"`,
		},
		{
			name:         "no host variant",
			errMsg:       "dial tcp: lookup missing: no host",
			host:         "missing",
			port:         5432,
			database:     "mydb",
			wantContains: `cannot resolve host "missing"`,
		},
		{
			name:         "password auth failed",
			errMsg:       `password authentication failed for user "postgres"`,
			host:         "localhost",
			port:         5432,
			database:     "testdb",
			wantContains: `password authentication failed for database "testdb"`,
		},
		{
			name:         "database does not exist",
			errMsg:       `database "nope" does not exist`,
			host:         "localhost",
			port:         5432,
			database:     "nope",
			wantContains: `database "nope" does not exist`,
		},
		{
			name:         "timeout",
			errMsg:       "dial tcp 10.0.0.1:5432: i/o timeout",
			host:         "10.0.0.1",
			port:         5432,
			database:     "mydb",
			wantContains: "connection timed out to 10.0.0.1:5432",
		},
		{
			name:         "timed out variant",
			errMsg:       "context deadline exceeded (timed out)",
			host:         "slow.host",
			port:         5432,
			database:     "mydb",
			wantContains: "connection timed out to slow.host:5432",
		},
		{
			name:         "SSL error",
			errMsg:       "SSL is not enabled on the server",
			host:         "localhost",
			port:         5432,
			database:     "mydb",
			wantContains: "SSL/TLS connection error",
		},
		{
			name:         "TLS error",
			errMsg:       "tls: handshake failure",
			host:         "localhost",
			port:         5432,
			database:     "mydb",
			wantContains: "SSL/TLS connection error",
		},
		{
			name:         "too many connections",
			errMsg:       "FATAL: too many connections for role",
			host:         "localhost",
			port:         5432,
			database:     "busydb",
			wantContains: `too many connections to database "busydb"`,
		},
		{
			name:         "unknown error falls through to default",
			errMsg:       "something completely unexpected happened",
			host:         "localhost",
			port:         5432,
			database:     "mydb",
			wantContains: "failed to connect to database",
		},
		{
			name:         "case insensitive matching",
			errMsg:       "CONNECTION REFUSED by firewall",
			host:         "firewall.host",
			port:         5433,
			database:     "mydb",
			wantContains: "connection refused to firewall.host:5433",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalErr := errors.New(tt.errMsg)
			wrapped := wrapConnectionError(originalErr, tt.host, tt.port, tt.database)

			if !strings.Contains(wrapped.Error(), tt.wantContains) {
				t.Errorf("wrapConnectionError() = %q, want it to contain %q", wrapped.Error(), tt.wantContains)
			}

			// Verify original error is wrapped (unwrappable)
			if !errors.Is(wrapped, originalErr) {
				t.Error("wrapped error does not unwrap to original error")
			}

			// Verify ErrConnectionFailed sentinel is chained
			if !errors.Is(wrapped, pgmi.ErrConnectionFailed) {
				t.Error("wrapped error does not chain pgmi.ErrConnectionFailed")
			}
		})
	}
}
