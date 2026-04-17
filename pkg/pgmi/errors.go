package pgmi

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Sentinel errors for common failure scenarios.
// These enable callers to distinguish error types using errors.Is().
//
// Example usage:
//
//	err := deployer.Deploy(ctx, config)
//	if errors.Is(err, pgmi.ErrApprovalDenied) {
//	    // Handle user denying approval
//	}
var (
	// ErrInvalidConfig indicates the provided configuration is invalid.
	ErrInvalidConfig = errors.New("invalid configuration")

	// ErrDeploySQLNotFound indicates the required deploy.sql file was not found.
	ErrDeploySQLNotFound = errors.New("deploy.sql not found")

	// ErrApprovalDenied indicates the user denied approval for the operation.
	ErrApprovalDenied = errors.New("approval denied")

	// ErrExecutionFailed indicates SQL execution failed.
	ErrExecutionFailed = errors.New("execution failed")

	// ErrUnsupportedAuthMethod indicates the requested authentication method is not supported.
	ErrUnsupportedAuthMethod = errors.New("unsupported authentication method")

	// ErrConnectionFailed indicates database connection failed.
	ErrConnectionFailed = errors.New("connection failed")
)

// ExitCodeForError returns the appropriate exit code for an error.
// Returns ExitSuccess (0) for nil errors, semantic codes for known errors,
// and ExitGeneralError (1) for unclassified errors.
func ExitCodeForError(err error) int {
	if err == nil {
		return ExitSuccess
	}

	// Check for sentinel errors
	switch {
	case errors.Is(err, ErrInvalidConfig):
		return ExitConfigError
	case errors.Is(err, ErrDeploySQLNotFound):
		return ExitDeploySQLMissing
	case errors.Is(err, ErrApprovalDenied):
		return ExitApprovalDenied
	case errors.Is(err, ErrExecutionFailed):
		return ExitExecutionFailed
	case errors.Is(err, ErrConnectionFailed):
		return ExitConnectionError
	case errors.Is(err, ErrUnsupportedAuthMethod):
		return ExitConfigError
	}

	errStr := err.Error()

	// Check for Cobra usage errors
	if strings.Contains(errStr, "unknown flag") ||
		strings.Contains(errStr, "unknown shorthand flag") ||
		strings.Contains(errStr, "accepts ") ||
		strings.Contains(errStr, "required flag") ||
		strings.Contains(errStr, "invalid argument") ||
		strings.Contains(errStr, "missing required argument") {
		return ExitUsageError
	}

	// Check for common connection error patterns
	if strings.Contains(errStr, "failed to connect") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host") {
		return ExitConnectionError
	}

	return ExitGeneralError
}

// FormatError renders an error for CLI output. For plain errors it returns the
// message. If the chain contains a *pgconn.PgError, it appends the DETAIL,
// HINT, and WHERE context fields that PostgreSQL attached to the error but
// that err.Error() omits, matching the diagnostic fields psql surfaces.
func FormatError(err error) string {
	if err == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString(err.Error())

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Detail != "" {
			fmt.Fprintf(&b, "\nDETAIL: %s", pgErr.Detail)
		}
		if pgErr.Hint != "" {
			fmt.Fprintf(&b, "\nHINT: %s", pgErr.Hint)
		}
		if pgErr.Where != "" {
			fmt.Fprintf(&b, "\nWHERE: %s", pgErr.Where)
		}
	}

	return b.String()
}
