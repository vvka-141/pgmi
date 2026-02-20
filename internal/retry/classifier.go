package retry

import (
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL error codes for transient conditions.
// See: https://www.postgresql.org/docs/current/errcodes-appendix.html
//
// Classes 08 (Connection Exception), 53 (Insufficient Resources), and
// 57 (Operator Intervention) are matched by prefix in isTransientPgError.
// Only codes from other classes need individual constants.
const (
	// Class 40 - Transaction Rollback
	pgCodeSerializationFailure = "40001"
	pgCodeDeadlockDetected     = "40P01"

	// Class 55 - Object Not In Prerequisite State
	pgCodeLockNotAvailable = "55P03"
)

// PostgreSQLErrorClassifier implements ErrorClassifier for PostgreSQL-specific errors.
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
	// PostgreSQL error codes: https://www.postgresql.org/docs/current/errcodes-appendix.html
	code := pgErr.Code

	// Class 08 - Connection Exception
	if strings.HasPrefix(code, "08") {
		return true
	}

	// Class 53 - Insufficient Resources
	if strings.HasPrefix(code, "53") {
		return true
	}

	// Class 57 - Operator Intervention (admin shutdown, crash shutdown, etc.)
	if strings.HasPrefix(code, "57") {
		return true
	}

	// Individual codes from classes not covered by prefix
	switch code {
	case pgCodeSerializationFailure, pgCodeDeadlockDetected: // Class 40
		return true
	case pgCodeLockNotAvailable: // Class 55
		return true
	}

	return false
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
