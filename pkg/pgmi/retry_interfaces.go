package pgmi

import "time"

// ErrorClassifier determines whether an error is transient (retryable) or fatal.
type ErrorClassifier interface {
	// IsTransient returns true if the error is temporary and the operation should be retried.
	IsTransient(err error) bool
}

// BackoffStrategy calculates the delay before the next retry attempt.
type BackoffStrategy interface {
	// NextDelay returns the duration to wait before the next attempt.
	// attempt is zero-indexed (0 = first retry, 1 = second retry, etc.)
	NextDelay(attempt int) time.Duration

	// MaxAttempts returns the maximum number of retry attempts (0 = no retries, -1 = unlimited)
	MaxAttempts() int
}

