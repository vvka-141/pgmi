package retry

import (
	"errors"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgreSQL error codes for transient conditions
// See: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	// Class 08 - Connection Exception
	pgCodeConnectionException                        = "08000"
	pgCodeConnectionDoesNotExist                     = "08003"
	pgCodeConnectionFailure                          = "08006"
	pgCodeSQLClientUnableToEstablishConnection       = "08001"
	pgCodeSQLServerRejectedEstablishmentOfConnection = "08004"

	// Class 40 - Transaction Rollback
	pgCodeSerializationFailure = "40001"
	pgCodeDeadlockDetected     = "40P01"

	// Class 53 - Insufficient Resources
	pgCodeInsufficientResources         = "53000"
	pgCodeDiskFull                      = "53100"
	pgCodeOutOfMemory                   = "53200"
	pgCodeTooManyConnections            = "53300"
	pgCodeConfigurationLimitExceeded    = "53400"

	// Class 55 - Object Not In Prerequisite State
	pgCodeLockNotAvailable = "55P03"

	// Class 57 - Operator Intervention
	pgCodeAdminShutdown    = "57P01"
	pgCodeCrashShutdown    = "57P02"
	pgCodeCannotConnectNow = "57P03"
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

	// Check for specific transient error codes
	switch code {
	// Class 08 - Connection Exception
	case pgCodeConnectionException,
		pgCodeConnectionDoesNotExist,
		pgCodeConnectionFailure,
		pgCodeSQLClientUnableToEstablishConnection,
		pgCodeSQLServerRejectedEstablishmentOfConnection:
		return true

	// Class 40 - Transaction Rollback
	case pgCodeSerializationFailure,
		pgCodeDeadlockDetected:
		return true

	// Class 53 - Insufficient Resources
	case pgCodeInsufficientResources,
		pgCodeDiskFull,
		pgCodeOutOfMemory,
		pgCodeTooManyConnections,
		pgCodeConfigurationLimitExceeded:
		return true

	// Class 55 - Object Not In Prerequisite State
	case pgCodeLockNotAvailable:
		return true

	// Class 57 - Operator Intervention
	case pgCodeAdminShutdown,
		pgCodeCrashShutdown,
		pgCodeCannotConnectNow:
		return true
	}

	return false
}

// isNetworkError checks for network-level errors.
func (c *PostgreSQLErrorClassifier) isNetworkError(err error) bool {
	// DNS errors
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		// Temporary DNS failures are retryable
		return dnsErr.Temporary() || dnsErr.Timeout()
	}

	// Network operation errors
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// Temporary network errors are retryable
		if opErr.Temporary() || opErr.Timeout() {
			return true
		}

		// Check underlying error
		if opErr.Err != nil {
			// Connection refused (server not ready)
			if errors.Is(opErr.Err, syscall.ECONNREFUSED) {
				return true
			}

			// Connection reset by peer
			if errors.Is(opErr.Err, syscall.ECONNRESET) {
				return true
			}

			// Network unreachable
			if errors.Is(opErr.Err, syscall.ENETUNREACH) {
				return true
			}

			// Host unreachable
			if errors.Is(opErr.Err, syscall.EHOSTUNREACH) {
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
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"broken pipe",
		"too many connections",
		"server closed the connection",
		"unexpected eof",
		"connection pool exhausted",
		"context deadline exceeded", // May be transient if external timeout
	}

	for _, pattern := range transientPatterns {
		if strings.Contains(strings.ToLower(errMsg), pattern) {
			return true
		}
	}

	return false
}
