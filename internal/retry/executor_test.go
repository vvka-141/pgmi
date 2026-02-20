package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// mockOperation tracks invocation count and simulates transient failures
type mockOperation struct {
	invocations   int
	failUntil     int // Fail for invocations < failUntil
	transientErr  error
	fatalErr      error
}

func (m *mockOperation) execute(ctx context.Context) error {
	m.invocations++

	if m.invocations < m.failUntil {
		if m.transientErr != nil {
			return m.transientErr
		}
		return &pgconn.PgError{Code: "08006", Message: "connection failure"}
	}

	if m.invocations == m.failUntil && m.fatalErr != nil {
		return m.fatalErr
	}

	return nil // Success
}

func TestExecutor_Execute_SuccessOnFirstAttempt(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(3, WithJitter(0))


	executor := NewExecutor(classifier, strategy)

	op := &mockOperation{failUntil: 1} // Succeed immediately

	err := executor.Execute(context.Background(), op.execute)

	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}
	if op.invocations != 1 {
		t.Errorf("Expected 1 invocation, got %d", op.invocations)
	}
}

func TestExecutor_Execute_SuccessAfterRetries(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(5,
		WithInitialDelay(1*time.Millisecond), // Use short delays for faster tests
		WithJitter(0),
	)

	executor := NewExecutor(classifier, strategy) // Use real time for this test

	// Fail first 3 attempts, succeed on 4th
	op := &mockOperation{failUntil: 4}

	err := executor.Execute(context.Background(), op.execute)

	if err != nil {
		t.Errorf("Expected success after retries, got error: %v", err)
	}
	if op.invocations != 4 {
		t.Errorf("Expected 4 invocations, got %d", op.invocations)
	}
}

func TestExecutor_Execute_FatalErrorNoRetry(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(5)

	executor := NewExecutor(classifier, strategy)

	fatalErr := &pgconn.PgError{Code: "42601", Message: "syntax error"}
	op := &mockOperation{failUntil: 2, transientErr: fatalErr} // Always fail with fatal error

	err := executor.Execute(context.Background(), op.execute)

	if err == nil {
		t.Fatal("Expected fatal error, got nil")
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42601" {
		t.Errorf("Expected PgError with code 42601, got %v", err)
	}

	if op.invocations != 1 {
		t.Errorf("Expected 1 invocation (no retries for fatal error), got %d", op.invocations)
	}
}

func TestExecutor_Execute_ExhaustedRetries(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(3, // Max 3 retries
		WithInitialDelay(1*time.Millisecond),
		WithJitter(0),
	)

	executor := NewExecutor(classifier, strategy)

	// Never succeed (always return transient error)
	transientErr := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	op := &mockOperation{failUntil: 999, transientErr: transientErr}

	err := executor.Execute(context.Background(), op.execute)

	if err == nil {
		t.Fatal("Expected error after exhausted retries, got nil")
	}

	// Initial attempt + 3 retries = 4 invocations
	expectedInvocations := 4
	if op.invocations != expectedInvocations {
		t.Errorf("Expected %d invocations (1 initial + 3 retries), got %d",
			expectedInvocations, op.invocations)
	}
}

func TestExecutor_Execute_ContextCancellation(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(10,
		WithInitialDelay(1*time.Second), // Long delay
	)


	executor := NewExecutor(classifier, strategy)

	ctx, cancel := context.WithCancel(context.Background())

	op := &mockOperation{failUntil: 999} // Always fail

	// Cancel context after first failure
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := executor.Execute(ctx, op.execute)

	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}

	// Should have initial attempt and possibly started retry before cancellation
	if op.invocations < 1 {
		t.Errorf("Expected at least 1 invocation, got %d", op.invocations)
	}
	if op.invocations > 2 {
		t.Errorf("Expected at most 2 invocations (cancelled during wait), got %d", op.invocations)
	}
}

func TestExecutor_Execute_TransientThenFatal(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(5,
		WithInitialDelay(1*time.Millisecond),
		WithJitter(0),
	)

	executor := NewExecutor(classifier, strategy)

	// Transient error for first 2 attempts, then fatal error
	transientErr := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	fatalErr := &pgconn.PgError{Code: "42601", Message: "syntax error"}

	invocations := 0
	operation := func(ctx context.Context) error {
		invocations++
		if invocations < 3 {
			return transientErr
		}
		return fatalErr
	}

	err := executor.Execute(context.Background(), operation)

	if err != fatalErr {
		t.Errorf("Expected fatal error, got %v", err)
	}

	// Should stop immediately when fatal error occurs (no more retries)
	if invocations != 3 {
		t.Errorf("Expected 3 invocations (2 transient + 1 fatal), got %d", invocations)
	}
}

func TestExecutor_Execute_OnRetryCallback(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(3,
		WithInitialDelay(1*time.Millisecond),
		WithJitter(0),
	)

	var retryAttempts []int
	var retryErrors []error
	var retryDelays []time.Duration

	onRetry := func(attempt int, err error, delay time.Duration) {
		retryAttempts = append(retryAttempts, attempt)
		retryErrors = append(retryErrors, err)
		retryDelays = append(retryDelays, delay)
	}

	executor := NewExecutor(classifier, strategy).WithOnRetry(onRetry)

	// Fail 3 times, succeed on 4th
	op := &mockOperation{failUntil: 4}

	err := executor.Execute(context.Background(), op.execute)

	if err != nil {
		t.Fatalf("Expected success, got error: %v", err)
	}

	// Should have 3 retry callbacks (attempts 0, 1, 2)
	if len(retryAttempts) != 3 {
		t.Fatalf("Expected 3 retry callbacks, got %d", len(retryAttempts))
	}

	// Verify callback data
	expectedAttempts := []int{0, 1, 2}
	expectedDelays := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		4 * time.Millisecond,
	}

	for i := range retryAttempts {
		if retryAttempts[i] != expectedAttempts[i] {
			t.Errorf("Retry %d: expected attempt %d, got %d",
				i, expectedAttempts[i], retryAttempts[i])
		}
		if retryDelays[i] != expectedDelays[i] {
			t.Errorf("Retry %d: expected delay %v, got %v",
				i, expectedDelays[i], retryDelays[i])
		}
		if retryErrors[i] == nil {
			t.Errorf("Retry %d: expected error, got nil", i)
		}
	}
}

func TestExecutor_Execute_NoRetriesStrategy(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(0) // MaxAttempts = 0 (no retries)


	executor := NewExecutor(classifier, strategy)

	transientErr := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	op := &mockOperation{failUntil: 999, transientErr: transientErr}

	err := executor.Execute(context.Background(), op.execute)

	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	// Should only try once (no retries)
	if op.invocations != 1 {
		t.Errorf("Expected 1 invocation (no retries), got %d", op.invocations)
	}
}

func TestExecutor_Execute_GenericTransientError(t *testing.T) {
	classifier := NewPostgreSQLErrorClassifier()
	strategy := NewExponentialBackoff(3,
		WithInitialDelay(1*time.Millisecond),
		WithJitter(0),
	)

	executor := NewExecutor(classifier, strategy)

	// Generic network error (should be classified as transient)
	networkErr := errors.New("connection refused")

	invocations := 0
	operation := func(ctx context.Context) error {
		invocations++
		if invocations < 3 {
			return networkErr
		}
		return nil // Success on 3rd attempt
	}

	err := executor.Execute(context.Background(), operation)

	if err != nil {
		t.Errorf("Expected success after retries, got error: %v", err)
	}
	if invocations != 3 {
		t.Errorf("Expected 3 invocations, got %d", invocations)
	}
}
