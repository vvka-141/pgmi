package retry

import (
	"context"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// Executor orchestrates retry attempts with backoff and error classification.
//
// Thread Safety:
// The Executor itself is safe for concurrent use when calling Execute().
// However, WithOnRetry() returns a NEW instance with the callback configured,
// ensuring each goroutine can have its own configuration without shared state.
// The original Executor remains unchanged.
type Executor struct {
	classifier pgmi.ErrorClassifier
	strategy   pgmi.BackoffStrategy
	onRetry    func(attempt int, err error, delay time.Duration)
}

// NewExecutor creates a new retry executor with the given configuration.
// Panics if classifier or strategy is nil.
func NewExecutor(
	classifier pgmi.ErrorClassifier,
	strategy pgmi.BackoffStrategy,
) *Executor {
	if classifier == nil {
		panic("classifier cannot be nil")
	}
	if strategy == nil {
		panic("strategy cannot be nil")
	}
	return &Executor{
		classifier: classifier,
		strategy:   strategy,
	}
}

// WithOnRetry returns a new Executor with the specified retry callback.
//
// This method does NOT modify the receiver; it returns a new instance.
// This ensures thread-safety when configuring executors concurrently.
//
// Example:
//
//	executor := retry.NewExecutor(classifier, strategy)
//	executor1 := executor.WithOnRetry(callback1) // New instance
//	executor2 := executor.WithOnRetry(callback2) // Another new instance
//	// executor1 and executor2 are independent
func (e *Executor) WithOnRetry(callback func(attempt int, err error, delay time.Duration)) *Executor {
	clone := *e
	clone.onRetry = callback
	return &clone
}

// Execute runs the operation with retry logic.
// Returns the result of the last attempt (success or fatal error).
func (e *Executor) Execute(ctx context.Context, operation func(ctx context.Context) error) error {
	var lastErr error
	maxAttempts := e.strategy.MaxAttempts()

	// Initial attempt (not a retry)
	lastErr = operation(ctx)
	if lastErr == nil {
		return nil // Success on first attempt
	}

	// Check if error is retryable
	if !e.classifier.IsTransient(lastErr) {
		return lastErr // Fatal error, don't retry
	}

	// Retry loop: attempt retries until maxAttempts is reached
	// If maxAttempts is negative (typically -1), retry indefinitely
	for attempt := 0; maxAttempts < 0 || attempt < maxAttempts; attempt++ {
		// Check context cancellation before waiting
		if err := ctx.Err(); err != nil {
			return err
		}

		// Calculate backoff delay
		delay := e.strategy.NextDelay(attempt)

		// Call onRetry callback if provided
		if e.onRetry != nil {
			e.onRetry(attempt, lastErr, delay)
		}

		// Wait for backoff period (respecting context cancellation)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			// Continue to retry
		}

		// Execute retry attempt
		lastErr = operation(ctx)
		if lastErr == nil {
			return nil // Success
		}

		// Check if error is still retryable
		if !e.classifier.IsTransient(lastErr) {
			return lastErr // Fatal error, stop retrying
		}
	}

	// Exhausted all retry attempts
	return lastErr
}
