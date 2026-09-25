package retry

import (
	"context"
	"time"
)

// Executor retries an operation while its failures stay transient.
//
// WithOnRetry returns a copy, so an Executor is safe to share between goroutines.
type Executor struct {
	classifier *PostgreSQLErrorClassifier
	strategy   *ExponentialBackoff
	onRetry    func(attempt int, err error, delay time.Duration)
}

// NewExecutor creates a retry executor.
func NewExecutor(classifier *PostgreSQLErrorClassifier, strategy *ExponentialBackoff) *Executor {
	return &Executor{classifier: classifier, strategy: strategy}
}

// WithOnRetry returns a copy of the executor that calls callback before each wait.
func (e *Executor) WithOnRetry(callback func(attempt int, err error, delay time.Duration)) *Executor {
	clone := *e
	clone.onRetry = callback
	return &clone
}

// Execute runs operation once, then retries it up to the strategy's retry count
// while the error stays transient. It returns the last attempt's error.
func (e *Executor) Execute(ctx context.Context, operation func(ctx context.Context) error) error {
	lastErr := operation(ctx)
	for attempt := 0; lastErr != nil && e.classifier.IsTransient(lastErr) && attempt < e.strategy.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		delay := e.strategy.NextDelay(attempt)
		if e.onRetry != nil {
			e.onRetry(attempt, lastErr, delay)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		lastErr = operation(ctx)
	}
	return lastErr
}
