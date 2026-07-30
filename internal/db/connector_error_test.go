package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// pgxMultiAttemptError reproduces what pgx actually returns: every attempt it
// made, opening with the TLS refusal that a non-TLS server always produces.
// Captured live from pgx v5.10.0 against PostgreSQL 17.10.
func pgxMultiAttemptError(pgErr *pgconn.PgError) error {
	return fmt.Errorf("failed to connect to `user=u database=d`:\n"+
		"\t127.0.0.1:5442 (127.0.0.1): tls error: server refused TLS connection\n"+
		"\t127.0.0.1:5442 (127.0.0.1): server error: %w", pgErr)
}

// Every server-side refusal arrives wrapped in text containing "tls error", so
// classifying on prose reported them all as SSL problems.
func TestWrapConnectionErrorPrefersSQLSTATE(t *testing.T) {
	tests := []struct {
		name         string
		code         string
		message      string
		wantContains string
	}{
		{
			name:         "insufficient privilege is not an SSL problem",
			code:         "42501",
			message:      `permission denied for database "privtest"`,
			wantContains: `permission denied for database "privtest"`,
		},
		{
			name:         "too many connections is reachable again",
			code:         "53300",
			message:      "sorry, too many clients already",
			wantContains: `too many connections to database "mydb"`,
		},
		{
			name:         "invalid password",
			code:         "28P01",
			message:      `password authentication failed for user "u"`,
			wantContains: `password authentication failed for user "u" on database "mydb"`,
		},
		{
			name:         "no pg_hba entry",
			code:         "28000",
			message:      `no pg_hba.conf entry for host "10.0.0.9"`,
			wantContains: "no pg_hba.conf entry matches this host, user, and method",
		},
		{
			name:         "missing database",
			code:         "3D000",
			message:      `database "mydb" does not exist`,
			wantContains: `database "mydb" does not exist`,
		},
		{
			name:         "server still starting",
			code:         "57P03",
			message:      "the database system is starting up",
			wantContains: "is not accepting connections",
		},
		{
			name:         "unmapped SQLSTATE reports the server verbatim",
			code:         "58P01",
			message:      "could not open file",
			wantContains: "could not open file (SQLSTATE 58P01)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pgErr := &pgconn.PgError{Severity: "FATAL", Code: tt.code, Message: tt.message}
			original := pgxMultiAttemptError(pgErr)

			wrapped := wrapConnectionError(original, "127.0.0.1", 5442, "mydb", "u")

			if !strings.Contains(wrapped.Error(), tt.wantContains) {
				t.Errorf("wrapConnectionError() = %q, want it to contain %q", wrapped.Error(), tt.wantContains)
			}
			if strings.Contains(wrapped.Error(), "SSL/TLS connection error") {
				t.Errorf("SQLSTATE %s misreported as an SSL/TLS error: %q", tt.code, wrapped.Error())
			}
			if !errors.Is(wrapped, pgmi.ErrConnectionFailed) {
				t.Error("wrapped error does not chain pgmi.ErrConnectionFailed")
			}
			if !errors.As(wrapped, &pgErr) {
				t.Error("wrapped error no longer unwraps to the PgError")
			}
		})
	}
}

func TestWrapConnectionError(t *testing.T) {
	tests := []struct {
		name         string
		errMsg       string
		host         string
		port         int
		database     string
		username     string
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
			username:     "postgres",
			wantContains: `password authentication failed for user "postgres" on database "testdb"`,
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
			wrapped := wrapConnectionError(originalErr, tt.host, tt.port, tt.database, tt.username)

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
