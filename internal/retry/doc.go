// Package retry provides automatic retry logic with exponential backoff
// for transient database connection failures.
//
// The package supports pluggable error classification and backoff strategies,
// making it suitable for various retry scenarios beyond database connections.
//
// # Example Usage
//
//	classifier := retry.NewPostgreSQLErrorClassifier()
//	strategy := retry.NewExponentialBackoff(3)
//	executor := retry.NewExecutor(classifier, strategy)
//
//	err := executor.Execute(ctx, func(ctx context.Context) error {
//	    return connectToDatabase(ctx)
//	})
//
// # Error Classification
//
// The ErrorClassifier interface determines which errors are transient (retryable)
// versus fatal (non-retryable). The PostgreSQLErrorClassifier recognizes common
// transient PostgreSQL errors like connection refused, network failures, etc.
//
// # Backoff Strategies
//
// The BackoffStrategy interface controls retry timing. The ExponentialBackoffStrategy
// implements exponential backoff with configurable initial delay and maximum delay caps.
//
// # Thread Safety
//
// Executor instances are safe for concurrent use. Use WithOnRetry() to create
// independent configurations per goroutine.
package retry
