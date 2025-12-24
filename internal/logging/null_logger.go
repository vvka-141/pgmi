package logging

// NullLogger is a no-op logger that discards all log messages.
// Safe for concurrent use by multiple goroutines.
// Useful for testing and when logging is not desired.
type NullLogger struct{}

// NewNullLogger creates a new NullLogger.
func NewNullLogger() *NullLogger {
	return &NullLogger{}
}

// Verbose is a no-op.
func (l *NullLogger) Verbose(format string, args ...interface{}) {}

// Info is a no-op.
func (l *NullLogger) Info(format string, args ...interface{}) {}

// Error is a no-op.
func (l *NullLogger) Error(format string, args ...interface{}) {}
