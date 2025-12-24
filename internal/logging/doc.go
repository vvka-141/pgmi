// Package logging provides concrete implementations of the pgmi.Logger interface.
//
// Available implementations:
//   - ConsoleLogger: Writes formatted messages to stdout with thread-safe output
//   - NullLogger: Discards all messages (useful for testing)
//
// All logger implementations are safe for concurrent use by multiple goroutines.
package logging
