package retry

import (
	"testing"
	"time"
)

// TestExponentialBackoffStrategy_MaxDelayConstraint_NeverExceeds1Minute verifies
// that retry delays are always capped at 1 minute, regardless of attempt number.
func TestExponentialBackoffStrategy_MaxDelayConstraint_NeverExceeds1Minute(t *testing.T) {
	strategy := NewExponentialBackoff(100, // Many retries
		WithInitialDelay(100*time.Millisecond),
		WithMultiplier(2.0),
		WithMaxDelay(1*time.Minute), // Hard cap at 1 minute
		WithJitter(0),                // Disable jitter for deterministic testing
	)

	maxDelayAllowed := 1 * time.Minute

	// Test a wide range of retry attempts (0-100)
	for attempt := 0; attempt <= 100; attempt++ {
		delay := strategy.NextDelay(attempt)

		if delay > maxDelayAllowed {
			t.Errorf("Attempt %d: delay %v exceeds max allowed delay of %v",
				attempt, delay, maxDelayAllowed)
		}

		// For very high attempt numbers, delay should be exactly at the cap
		if attempt > 20 && delay != maxDelayAllowed {
			t.Errorf("Attempt %d: expected delay to be capped at %v, got %v",
				attempt, maxDelayAllowed, delay)
		}
	}

	t.Logf("Verified: all delays for attempts 0-100 are ≤ 1 minute")
}

// TestExponentialBackoffStrategy_DefaultMaxDelay verifies the default max delay.
func TestExponentialBackoffStrategy_DefaultMaxDelay(t *testing.T) {
	strategy := NewExponentialBackoff(10)

	// Default should be 30 seconds (from NewExponentialBackoff)
	expectedDefaultMax := 30 * time.Second

	if strategy.MaxDelay() != expectedDefaultMax {
		t.Errorf("Expected default MaxDelay=%v, got %v", expectedDefaultMax, strategy.MaxDelay())
	}
}

// TestExponentialBackoffStrategy_ProductionConfig simulates production retry configuration.
func TestExponentialBackoffStrategy_ProductionConfig(t *testing.T) {
	// This is the configuration used in StandardConnector
	strategy := NewExponentialBackoff(3,
		WithInitialDelay(100*time.Millisecond),
		WithMaxDelay(1*time.Minute),
		WithJitter(0), // Disable jitter for deterministic testing
	)

	// Verify the progression matches production expectations
	tests := []struct {
		attempt       int
		expectedDelay time.Duration
		maxDelay      time.Duration
	}{
		{attempt: 0, expectedDelay: 100 * time.Millisecond, maxDelay: 100 * time.Millisecond},
		{attempt: 1, expectedDelay: 200 * time.Millisecond, maxDelay: 200 * time.Millisecond},
		{attempt: 2, expectedDelay: 400 * time.Millisecond, maxDelay: 400 * time.Millisecond},
		{attempt: 10, expectedDelay: 1 * time.Minute, maxDelay: 1 * time.Minute}, // Should be capped
		{attempt: 50, expectedDelay: 1 * time.Minute, maxDelay: 1 * time.Minute}, // Should be capped
	}

	for _, tt := range tests {
		delay := strategy.NextDelay(tt.attempt)

		// Check that delay doesn't exceed expected value
		if delay > tt.maxDelay {
			t.Errorf("Attempt %d: delay %v exceeds max %v", tt.attempt, delay, tt.maxDelay)
		}

		// For low attempts, should match expected exactly (no jitter)
		if tt.attempt < 3 && delay != tt.expectedDelay {
			t.Errorf("Attempt %d: expected delay %v, got %v", tt.attempt, tt.expectedDelay, delay)
		}
	}

	if strategy.MaxAttempts() != 3 {
		t.Errorf("Expected MaxAttempts=3, got %d", strategy.MaxAttempts())
	}

	t.Log("Production retry configuration validated:")
	t.Logf("  - Max attempts: %d", strategy.MaxAttempts())
	t.Logf("  - Initial delay: %v", strategy.InitialDelay())
	t.Logf("  - Max delay: %v", strategy.MaxDelay())
	t.Logf("  - Multiplier: %.1f", strategy.Multiplier())
}

// TestExponentialBackoffStrategy_TotalRetryTime calculates total retry time.
func TestExponentialBackoffStrategy_TotalRetryTime(t *testing.T) {
	strategy := NewExponentialBackoff(3,
		WithInitialDelay(100*time.Millisecond),
		WithMaxDelay(1*time.Minute),
		WithJitter(0),
	)

	totalDelay := time.Duration(0)
	for attempt := 0; attempt < strategy.MaxAttempts(); attempt++ {
		delay := strategy.NextDelay(attempt)
		totalDelay += delay
		t.Logf("Retry %d: delay = %v (cumulative = %v)", attempt, delay, totalDelay)
	}

	// Expected: 100ms + 200ms + 400ms = 700ms
	expectedTotal := 700 * time.Millisecond
	if totalDelay != expectedTotal {
		t.Errorf("Expected total retry delay %v, got %v", expectedTotal, totalDelay)
	}

	t.Logf("Total retry time with 3 attempts: %v", totalDelay)
}

// TestExponentialBackoffStrategy_MaxDelayCapAtHighAttempts ensures cap is enforced
// even with extreme exponential growth.
func TestExponentialBackoffStrategy_MaxDelayCapAtHighAttempts(t *testing.T) {
	strategy := NewExponentialBackoff(50,
		WithInitialDelay(1*time.Second),
		WithMultiplier(3.0), // Aggressive multiplier
		WithMaxDelay(1*time.Minute),
		WithJitter(0),
	)

	// At attempt 10: 1s * 3^10 = 59,049 seconds (≈16 hours)
	// Should be capped at 1 minute
	delay := strategy.NextDelay(10)

	if delay != 1*time.Minute {
		t.Errorf("Expected delay capped at 1 minute, got %v", delay)
	}

	// Verify all high attempts are capped
	for attempt := 5; attempt <= 50; attempt++ {
		delay := strategy.NextDelay(attempt)
		if delay > 1*time.Minute {
			t.Errorf("Attempt %d: delay %v exceeds 1 minute cap", attempt, delay)
		}
	}

	t.Log("Max delay cap enforced correctly even with aggressive multiplier")
}
