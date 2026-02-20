package retry

import (
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgreSQLErrorClassifier_IsTransient_PostgreSQLErrors(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()

	tests := []struct {
		name       string
		err        error
		isTransient bool
	}{
		// Transient PostgreSQL errors
		{
			name:        "connection_exception (08000)",
			err:         &pgconn.PgError{Code: "08000", Message: "connection exception"},
			isTransient: true,
		},
		{
			name:        "connection_failure (08006)",
			err:         &pgconn.PgError{Code: "08006", Message: "connection failure"},
			isTransient: true,
		},
		{
			name:        "sqlclient_unable_to_establish_sqlconnection (08001)",
			err:         &pgconn.PgError{Code: "08001", Message: "sqlclient unable to establish connection"},
			isTransient: true,
		},
		{
			name:        "insufficient_resources (53000)",
			err:         &pgconn.PgError{Code: "53000", Message: "insufficient resources"},
			isTransient: true,
		},
		{
			name:        "too_many_connections (53300)",
			err:         &pgconn.PgError{Code: "53300", Message: "too many connections"},
			isTransient: true,
		},
		{
			name:        "serialization_failure (40001)",
			err:         &pgconn.PgError{Code: "40001", Message: "could not serialize access"},
			isTransient: true,
		},
		{
			name:        "deadlock_detected (40P01)",
			err:         &pgconn.PgError{Code: "40P01", Message: "deadlock detected"},
			isTransient: true,
		},
		{
			name:        "lock_not_available (55P03)",
			err:         &pgconn.PgError{Code: "55P03", Message: "could not obtain lock"},
			isTransient: true,
		},
		{
			name:        "admin_shutdown (57P01)",
			err:         &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"},
			isTransient: true,
		},
		{
			name:        "crash_shutdown (57P02)",
			err:         &pgconn.PgError{Code: "57P02", Message: "terminating connection due to crash"},
			isTransient: true,
		},
		{
			name:        "cannot_connect_now (57P03)",
			err:         &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"},
			isTransient: true,
		},

		// Fatal PostgreSQL errors
		{
			name:        "syntax_error (42601)",
			err:         &pgconn.PgError{Code: "42601", Message: "syntax error at or near"},
			isTransient: false,
		},
		{
			name:        "undefined_table (42P01)",
			err:         &pgconn.PgError{Code: "42P01", Message: "relation does not exist"},
			isTransient: false,
		},
		{
			name:        "unique_violation (23505)",
			err:         &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"},
			isTransient: false,
		},
		{
			name:        "foreign_key_violation (23503)",
			err:         &pgconn.PgError{Code: "23503", Message: "violates foreign key constraint"},
			isTransient: false,
		},
		{
			name:        "insufficient_privilege (42501)",
			err:         &pgconn.PgError{Code: "42501", Message: "permission denied"},
			isTransient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifier.IsTransient(tt.err)
			if result != tt.isTransient {
				t.Errorf("IsTransient(%v) = %v, want %v", tt.err, result, tt.isTransient)
			}
		})
	}
}

func TestPostgreSQLErrorClassifier_IsTransient_NetworkErrors(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()

	tests := []struct {
		name        string
		err         error
		isTransient bool
	}{
		{
			name:        "connection_refused",
			err:         &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
			isTransient: true,
		},
		{
			name:        "connection_reset",
			err:         &net.OpError{Op: "read", Err: syscall.ECONNRESET},
			isTransient: true,
		},
		{
			name:        "network_unreachable",
			err:         &net.OpError{Op: "dial", Err: syscall.ENETUNREACH},
			isTransient: true,
		},
		{
			name:        "host_unreachable",
			err:         &net.OpError{Op: "dial", Err: syscall.EHOSTUNREACH},
			isTransient: true,
		},
		{
			name: "dns_not_found_is_not_transient",
			err: &net.DNSError{
				Err:        "no such host",
				IsNotFound: true,
			},
			isTransient: false,
		},
		{
			name: "dns_temporary_error",
			err: &net.DNSError{
				Err:         "server misbehaving",
				IsTemporary: true,
			},
			isTransient: true,
		},
		{
			name: "dns_timeout",
			err: &net.DNSError{
				Err:       "timeout",
				IsTimeout: true,
			},
			isTransient: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifier.IsTransient(tt.err)
			if result != tt.isTransient {
				t.Errorf("IsTransient(%v) = %v, want %v", tt.err, result, tt.isTransient)
			}
		})
	}
}

func TestPostgreSQLErrorClassifier_IsTransient_ConnectionStringErrors(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()

	tests := []struct {
		name        string
		err         error
		isTransient bool
	}{
		{
			name:        "connection_refused_message",
			err:         errors.New("connection refused"),
			isTransient: true,
		},
		{
			name:        "connection_reset_message",
			err:         errors.New("connection reset by peer"),
			isTransient: true,
		},
		{
			name:        "connection_timeout_message",
			err:         errors.New("connection timeout"),
			isTransient: true,
		},
		{
			name:        "no_such_host_not_transient",
			err:         errors.New("no such host"),
			isTransient: false,
		},
		{
			name:        "network_unreachable_message",
			err:         errors.New("network is unreachable"),
			isTransient: true,
		},
		{
			name:        "io_timeout",
			err:         errors.New("i/o timeout"),
			isTransient: true,
		},
		{
			name:        "broken_pipe",
			err:         errors.New("broken pipe"),
			isTransient: true,
		},
		{
			name:        "too_many_connections_message",
			err:         errors.New("too many connections"),
			isTransient: true,
		},
		{
			name:        "server_closed_connection",
			err:         errors.New("server closed the connection unexpectedly"),
			isTransient: true,
		},
		{
			name:        "unexpected_eof",
			err:         errors.New("unexpected EOF"),
			isTransient: true,
		},
		{
			name:        "connection_pool_exhausted",
			err:         errors.New("connection pool exhausted"),
			isTransient: true,
		},
		// Non-transient errors
		{
			name:        "context_deadline_exceeded",
			err:         errors.New("context deadline exceeded"),
			isTransient: false,
		},
		{
			name:        "generic_error",
			err:         errors.New("some other error"),
			isTransient: false,
		},
		{
			name:        "nil_error",
			err:         nil,
			isTransient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifier.IsTransient(tt.err)
			if result != tt.isTransient {
				t.Errorf("IsTransient(%v) = %v, want %v", tt.err, result, tt.isTransient)
			}
		})
	}
}

func TestPostgreSQLErrorClassifier_IsTransient_WrappedErrors(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()

	// Test wrapped PostgreSQL error
	pgErr := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	wrappedPgErr := errors.New("wrapped: " + pgErr.Error())

	// Direct PgError should be detected
	if !classifier.IsTransient(pgErr) {
		t.Errorf("Expected direct PgError to be transient")
	}

	// Wrapped error should be detected via error message pattern
	if !classifier.IsTransient(wrappedPgErr) {
		t.Errorf("Expected wrapped error with 'connection' in message to be transient")
	}
}
