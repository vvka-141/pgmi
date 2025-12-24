package pgmi

// Logger provides a pluggable logging interface for pgmi operations.
// Implementations must be safe for concurrent use by multiple goroutines.
type Logger interface {
	// Verbose logs detailed diagnostic information.
	// Only logged when verbose mode is enabled.
	Verbose(format string, args ...interface{})

	// Info logs informational messages about normal operations.
	// Always logged regardless of verbose mode.
	Info(format string, args ...interface{})

	// Error logs error messages.
	// Always logged regardless of verbose mode.
	Error(format string, args ...interface{})
}
