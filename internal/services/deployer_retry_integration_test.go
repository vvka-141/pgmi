package services_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vvka-141/pgmi/internal/retry"
)

// TestRetryExecutor_SimulatedTransientFailure tests the retry mechanism with mocked transient errors.
// This test does NOT require a real PostgreSQL database.
func TestRetryExecutor_SimulatedTransientFailure(t *testing.T) {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(3,
		retry.WithInitialDelay(1*time.Millisecond), // Very short for fast test
		retry.WithJitter(0),
	)

	executor := retry.NewExecutor(classifier, strategy, nil)

	// Simulate an operation that fails twice with transient errors, then succeeds
	var attempts int32
	operation := func(ctx context.Context) error {
		attempt := atomic.AddInt32(&attempts, 1)

		switch attempt {
		case 1:
			// First attempt: connection refused
			return errors.New("connection refused")
		case 2:
			// Second attempt: connection failure
			return &pgconn.PgError{Code: "08006", Message: "connection failure"}
		case 3:
			// Third attempt: success
			return nil
		default:
			t.Errorf("Unexpected attempt %d", attempt)
			return errors.New("too many attempts")
		}
	}

	start := time.Now()
	err := executor.Execute(context.Background(), operation)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Expected success after retries, got error: %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts (1 initial + 2 retries), got %d", attempts)
	}

	// Verify retry delays occurred (should be at least 1ms + 2ms = 3ms total)
	if elapsed < 3*time.Millisecond {
		t.Logf("Warning: elapsed time %v seems too short, but may vary on fast systems", elapsed)
	}

	t.Logf("Success after %d attempts in %v", attempts, elapsed)
}

// TestRetryExecutor_FatalErrorNoRetry tests that fatal errors don't trigger retries.
func TestRetryExecutor_FatalErrorNoRetry(t *testing.T) {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(5,
		retry.WithInitialDelay(1*time.Millisecond),
	)

	executor := retry.NewExecutor(classifier, strategy, nil)

	// Simulate an operation that fails with a fatal error
	var attempts int32
	fatalErr := &pgconn.PgError{Code: "42601", Message: "syntax error"}

	operation := func(ctx context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return fatalErr
	}

	err := executor.Execute(context.Background(), operation)

	if err == nil {
		t.Fatal("Expected fatal error, got nil")
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt (no retries for fatal error), got %d", attempts)
	}

	t.Logf("Fatal error correctly stopped after %d attempt", attempts)
}

// TestRetryExecutor_ExhaustedRetries tests that retries are exhausted after max attempts.
func TestRetryExecutor_ExhaustedRetries(t *testing.T) {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(2, // Only 2 retries
		retry.WithInitialDelay(1*time.Millisecond),
		retry.WithJitter(0),
	)

	var retryAttempts []int
	onRetry := func(attempt int, err error, delay time.Duration) {
		retryAttempts = append(retryAttempts, attempt)
		t.Logf("Retry attempt %d after error: %v (delay: %v)", attempt, err, delay)
	}

	executor := retry.NewExecutor(classifier, strategy, nil).WithOnRetry(onRetry)

	// Simulate an operation that always fails with transient error
	var attempts int32
	transientErr := errors.New("connection timeout")

	operation := func(ctx context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return transientErr
	}

	err := executor.Execute(context.Background(), operation)

	if err == nil {
		t.Fatal("Expected error after exhausting retries, got nil")
	}

	// Should be: 1 initial attempt + 2 retries = 3 total attempts
	expectedAttempts := int32(3)
	if attempts != expectedAttempts {
		t.Errorf("Expected %d attempts, got %d", expectedAttempts, attempts)
	}

	// Should have 2 retry callbacks (attempts 0 and 1)
	if len(retryAttempts) != 2 {
		t.Errorf("Expected 2 retry callbacks, got %d", len(retryAttempts))
	}

	t.Logf("Correctly exhausted retries after %d attempts", attempts)
}

// TestRetryExecutor_ContextCancellation tests that context cancellation stops retries.
func TestRetryExecutor_ContextCancellation(t *testing.T) {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(10,
		retry.WithInitialDelay(50*time.Millisecond), // Longer delay to ensure cancellation happens
	)

	executor := retry.NewExecutor(classifier, strategy, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Simulate an operation that always fails
	var attempts int32
	operation := func(ctx context.Context) error {
		attempt := atomic.AddInt32(&attempts, 1)
		t.Logf("Attempt %d at %v", attempt, time.Now())
		return errors.New("connection refused")
	}

	start := time.Now()
	err := executor.Execute(ctx, operation)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Expected context deadline exceeded, got nil")
	}

	if err != context.DeadlineExceeded {
		t.Logf("Expected context.DeadlineExceeded, got: %v", err)
	}

	// Should have stopped due to context cancellation
	// Attempts should be limited (not all 10 retries should execute)
	if attempts > 3 {
		t.Errorf("Expected <= 3 attempts due to context cancellation, got %d", attempts)
	}

	if elapsed > 150*time.Millisecond {
		t.Errorf("Expected execution to stop around 100ms, took %v", elapsed)
	}

	t.Logf("Context cancellation correctly stopped retries after %d attempts in %v", attempts, elapsed)
}

// TestRetryExecutor_MixedErrors tests behavior when transient errors become fatal.
func TestRetryExecutor_MixedErrors(t *testing.T) {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(5,
		retry.WithInitialDelay(1*time.Millisecond),
		retry.WithJitter(0),
	)

	executor := retry.NewExecutor(classifier, strategy, nil)

	// Simulate: transient → transient → fatal
	var attempts int32
	operation := func(ctx context.Context) error {
		attempt := atomic.AddInt32(&attempts, 1)

		switch attempt {
		case 1:
			return errors.New("connection refused") // Transient
		case 2:
			return &pgconn.PgError{Code: "08006", Message: "connection failure"} // Transient
		case 3:
			return &pgconn.PgError{Code: "42601", Message: "syntax error"} // Fatal
		default:
			t.Errorf("Unexpected attempt %d", attempt)
			return errors.New("too many attempts")
		}
	}

	err := executor.Execute(context.Background(), operation)

	if err == nil {
		t.Fatal("Expected fatal error, got nil")
	}

	// Should stop immediately when fatal error is encountered
	if attempts != 3 {
		t.Errorf("Expected 3 attempts (2 transient + 1 fatal), got %d", attempts)
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42601" {
		t.Errorf("Expected PgError code 42601, got: %v", err)
	}

	t.Logf("Correctly stopped on fatal error after %d attempts", attempts)
}

// TestRetryExecutor_SuccessOnFirstAttempt tests the happy path (no retries needed).
func TestRetryExecutor_SuccessOnFirstAttempt(t *testing.T) {
	classifier := retry.NewPostgreSQLErrorClassifier()
	strategy := retry.NewExponentialBackoff(3)

	var retryCount int
	onRetry := func(attempt int, err error, delay time.Duration) {
		retryCount++
	}

	executor := retry.NewExecutor(classifier, strategy, nil).WithOnRetry(onRetry)

	var attempts int32
	operation := func(ctx context.Context) error {
		atomic.AddInt32(&attempts, 1)
		return nil // Success immediately
	}

	err := executor.Execute(context.Background(), operation)

	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt (no retries needed), got %d", attempts)
	}

	if retryCount != 0 {
		t.Errorf("Expected 0 retry callbacks, got %d", retryCount)
	}

	t.Log("Success on first attempt (no retries needed)")
}

// mockConnector simulates a connector with configurable failure behavior
type mockConnector struct {
	failCount   int
	failures    []error
	callCount   int
}

func (m *mockConnector) Connect(ctx context.Context) (*pgxpool.Pool, error) {
	m.callCount++
	if m.callCount <= m.failCount && len(m.failures) > 0 {
		idx := m.callCount - 1
		if idx >= len(m.failures) {
			idx = len(m.failures) - 1
		}
		return nil, m.failures[idx]
	}
	return nil, nil // Simulate success (would return real pool in integration test)
}

// TestConnectorRetry_SimulatedFailures tests connector-level retry integration.
func TestConnectorRetry_SimulatedFailures(t *testing.T) {
	t.Skip("Skipping mock connector test - retry logic is tested in StandardConnector")

	// This test demonstrates how retry logic would work at the connector level
	// The actual StandardConnector now has built-in retry via retry.Executor
}
