package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/internal/retry"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestStandardConnector_RetryConfiguration(t *testing.T) {
	config := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
		Password: "testpass",
	}

	connector := NewStandardConnector(config)

	if connector.retryExecutor == nil {
		t.Fatal("Expected retryExecutor to be initialized")
	}

	if connector.config != config {
		t.Error("Expected config to be set")
	}
}

func TestStandardConnector_RetryDefaults(t *testing.T) {
	config := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
		Password: "testpass",
	}

	connector := NewStandardConnector(config)

	// Verify retry executor is configured (non-nil check)
	if connector.retryExecutor == nil {
		t.Fatal("Expected retryExecutor to be initialized with default strategy")
	}

	// The retry strategy should have sensible defaults:
	// - 3 retry attempts
	// - Initial delay 100ms
	// - Max delay 5s
	// These are tested in the retry package unit tests
}

// Test error classification integration
func TestErrorClassification_Integration(t *testing.T) {
	classifier := retry.NewPostgreSQLErrorClassifier()

	// Test that classifier correctly identifies errors
	tests := []struct {
		name        string
		err         error
		expectRetry bool
	}{
		{
			name:        "connection refused is retryable",
			err:         errors.New("connection refused"),
			expectRetry: true,
		},
		{
			name:        "network unreachable is retryable",
			err:         errors.New("network is unreachable"),
			expectRetry: true,
		},
		{
			name:        "generic error is not retryable",
			err:         errors.New("some unrelated error"),
			expectRetry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isTransient := classifier.IsTransient(tt.err)
			if isTransient != tt.expectRetry {
				t.Errorf("Expected IsTransient=%v for error %q, got %v",
					tt.expectRetry, tt.err.Error(), isTransient)
			}
		})
	}
}

// Test backoff strategy integration
func TestBackoffStrategy_Integration(t *testing.T) {
	strategy := retry.NewExponentialBackoff(3,
		retry.WithInitialDelay(100*time.Millisecond),
		retry.WithMaxDelay(1*time.Minute), // Max delay capped at 1 minute per requirement
		retry.WithJitter(0),                // Disable jitter for deterministic testing
	)

	// Verify backoff progression
	expectedDelays := []time.Duration{
		100 * time.Millisecond,  // Attempt 0
		200 * time.Millisecond,  // Attempt 1
		400 * time.Millisecond,  // Attempt 2
	}

	for i, expected := range expectedDelays {
		actual := strategy.NextDelay(i)
		if actual != expected {
			t.Errorf("Attempt %d: expected delay %v, got %v", i, expected, actual)
		}
	}

	if strategy.MaxAttempts() != 3 {
		t.Errorf("Expected MaxAttempts=3, got %d", strategy.MaxAttempts())
	}

	// Verify max delay constraint: even for very large attempt numbers,
	// delay should never exceed 1 minute
	for attempt := 10; attempt <= 20; attempt++ {
		delay := strategy.NextDelay(attempt)
		if delay > 1*time.Minute {
			t.Errorf("Attempt %d: delay %v exceeds max delay of 1 minute", attempt, delay)
		}
	}
}

// Benchmark retry overhead
func BenchmarkStandardConnector_NoRetry(b *testing.B) {
	config := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "testuser",
		Password: "testpass",
	}

	connector := NewStandardConnector(config)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// This will fail immediately with connection error, but we measure the retry overhead
		_, _ = connector.Connect(context.Background())
	}
}
