package pgmi

import (
	"errors"
	"strings"
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

	// ErrNotImplemented indicates a feature is not yet implemented.
	ErrNotImplemented = errors.New("not implemented")

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

	// Check for common connection error patterns
	errStr := err.Error()
	if strings.Contains(errStr, "failed to connect") ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host") {
		return ExitConnectionError
	}

	return ExitGeneralError
}
