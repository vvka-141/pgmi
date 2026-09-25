package retry

import (
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATEs a server sends while refusing a new connection that a later attempt
// can succeed past. Class 08 is matched by prefix in isTransientPgError.
//
// Only connection establishment is retried. Statement-level conditions such as
// serialization failures, deadlocks and lock timeouts never reach this code,
// and retrying them is deploy.sql's decision, not pgmi's.
const (
	pgCodeTooManyConnections = "53300"
	pgCodeCannotConnectNow   = "57P03"
)

// PostgreSQLErrorClassifier decides whether a failed connection attempt is worth repeating.
type PostgreSQLErrorClassifier struct{}

// NewPostgreSQLErrorClassifier creates a new PostgreSQL error classifier.
func NewPostgreSQLErrorClassifier() *PostgreSQLErrorClassifier {
	return &PostgreSQLErrorClassifier{}
}

// IsTransient determines if an error is temporary and retryable.
func (c *PostgreSQLErrorClassifier) IsTransient(err error) bool {
	if err == nil {
		return false
	}

	// Check for PostgreSQL-specific errors
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return c.isTransientPgError(pgErr)
	}

	// Check for network-level errors
	if c.isNetworkError(err) {
		return true
	}

	// Check for connection errors
	if c.isConnectionError(err) {
		return true
	}

	return false
}

// isTransientPgError checks PostgreSQL error codes for transient conditions.
func (c *PostgreSQLErrorClassifier) isTransientPgError(pgErr *pgconn.PgError) bool {
	return strings.HasPrefix(pgErr.Code, "08") ||
		pgErr.Code == pgCodeTooManyConnections ||
		pgErr.Code == pgCodeCannotConnectNow
}

// isNetworkError checks for network-level errors.
func (c *PostgreSQLErrorClassifier) isNetworkError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsTimeout || dnsErr.IsTemporary
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return true
		}

		if opErr.Err != nil {
			if errors.Is(opErr.Err, syscall.ECONNREFUSED) ||
				errors.Is(opErr.Err, syscall.ECONNRESET) ||
				errors.Is(opErr.Err, syscall.ENETUNREACH) ||
				errors.Is(opErr.Err, syscall.EHOSTUNREACH) {
				return true
			}
		}
	}

	return false
}

// isConnectionError checks for connection-related errors from pgconn.
func (c *PostgreSQLErrorClassifier) isConnectionError(err error) bool {
	errMsg := err.Error()

	// Check for common connection error messages
	transientPatterns := []string{
		"connection refused",
		"connection reset",
		"connection timeout",
		"connection failure",
		"network is unreachable",
		"i/o timeout",
		"broken pipe",
		"too many connections",
		"server closed the connection",
		"unexpected eof",
		"connection pool exhausted",
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(strings.ToLower(errMsg), pattern) {
			return true
		}
	}

	return false
}
