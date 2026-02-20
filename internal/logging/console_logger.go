// Package logging provides concrete implementations of the pgmi.Logger interface.
package logging

import (
	"fmt"
	"os"
	"sync"
)

// ConsoleLogger writes log messages to stderr.
// Safe for concurrent use by multiple goroutines.
type ConsoleLogger struct {
	verbose bool
	mu      sync.Mutex
}

// NewConsoleLogger creates a new ConsoleLogger.
// If verbose is true, Verbose() calls will produce output.
// If verbose is false, Verbose() calls are no-ops.
func NewConsoleLogger(verbose bool) *ConsoleLogger {
	return &ConsoleLogger{
		verbose: verbose,
	}
}

// Verbose logs detailed diagnostic information if verbose mode is enabled.
func (l *ConsoleLogger) Verbose(format string, args ...interface{}) {
	if !l.verbose {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "[VERBOSE] "+format+"\n", args...)
	} else {
		fmt.Fprint(os.Stderr, "[VERBOSE] "+format+"\n")
	}
}

// Info logs informational messages about normal operations.
func (l *ConsoleLogger) Info(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	} else {
		fmt.Fprint(os.Stderr, format+"\n")
	}
}

// Error logs error messages.
func (l *ConsoleLogger) Error(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "[ERROR] "+format+"\n", args...)
	} else {
		fmt.Fprint(os.Stderr, "[ERROR] "+format+"\n")
	}
}
