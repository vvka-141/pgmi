package retry

import (
	"testing"
	"time"
)

func TestExponentialBackoffStrategy_DefaultValues(t *testing.T) {
	strategy := NewExponentialBackoff(3)

	if strategy.InitialDelay() != 100*time.Millisecond {
		t.Errorf("Expected InitialDelay=100ms, got %v", strategy.InitialDelay())
	}
	if strategy.MaxDelay() != 30*time.Second {
		t.Errorf("Expected MaxDelay=30s, got %v", strategy.MaxDelay())
	}
	if strategy.Multiplier() != 2.0 {
		t.Errorf("Expected Multiplier=2.0, got %v", strategy.Multiplier())
	}
	if strategy.Jitter() != 0.1 {
		t.Errorf("Expected Jitter=0.1, got %v", strategy.Jitter())
	}
	if strategy.MaxAttempts() != 3 {
		t.Errorf("Expected MaxAttempts=3, got %v", strategy.MaxAttempts())
	}
}

func TestExponentialBackoffStrategy_NextDelay_WithoutJitter(t *testing.T) {
	strategy := NewExponentialBackoff(5,
		WithInitialDelay(100*time.Millisecond),
		WithMultiplier(2.0),
		WithJitter(0), // Disable jitter for deterministic testing
	)

	tests := []struct {
		attempt      int
		expectedDelay time.Duration
	}{
		{attempt: 0, expectedDelay: 100 * time.Millisecond},  // 100 * 2^0
		{attempt: 1, expectedDelay: 200 * time.Millisecond},  // 100 * 2^1
		{attempt: 2, expectedDelay: 400 * time.Millisecond},  // 100 * 2^2
		{attempt: 3, expectedDelay: 800 * time.Millisecond},  // 100 * 2^3
		{attempt: 4, expectedDelay: 1600 * time.Millisecond}, // 100 * 2^4
	}

	for _, tt := range tests {
		delay := strategy.NextDelay(tt.attempt)
		if delay != tt.expectedDelay {
			t.Errorf("NextDelay(%d) = %v, want %v", tt.attempt, delay, tt.expectedDelay)
		}
	}
}

func TestExponentialBackoffStrategy_NextDelay_MaxDelayCap(t *testing.T) {
	strategy := NewExponentialBackoff(10,
		WithInitialDelay(100*time.Millisecond),
		WithMultiplier(2.0),
		WithMaxDelay(1*time.Second),
		WithJitter(0), // Disable jitter
	)

	// Attempt 10: 100ms * 2^10 = 102,400ms = 102.4s
	// Should be capped at MaxDelay = 1s
	delay := strategy.NextDelay(10)
	if delay != 1*time.Second {
		t.Errorf("NextDelay(10) = %v, want %v (should be capped at MaxDelay)", delay, 1*time.Second)
	}
}

func TestExponentialBackoffStrategy_NextDelay_WithJitter(t *testing.T) {
	jitterValues := []float64{0.0, 0.5, 1.0}
	expectedDelays := make([]time.Duration, len(jitterValues))

	for i, jv := range jitterValues {
		strategy := NewExponentialBackoff(3,
			WithInitialDelay(100*time.Millisecond),
			WithMultiplier(2.0),
			WithJitter(0.1),
			WithJitterFunc(func() float64 { return jv }), // Deterministic jitter
		)

		expectedDelays[i] = strategy.NextDelay(0)
	}

	// With jitter=0.1:
	// jv=0.0 => randomOffset=-1.0 => factor=1+0.1*(-1)=0.9 => 100ms*0.9=90ms
	// jv=0.5 => randomOffset=0.0  => factor=1+0.1*0=1.0   => 100ms*1.0=100ms
	// jv=1.0 => randomOffset=1.0  => factor=1+0.1*1=1.1   => 100ms*1.1=110ms

	if expectedDelays[0] != 90*time.Millisecond {
		t.Errorf("NextDelay with jv=0.0 = %v, want 90ms", expectedDelays[0])
	}
	if expectedDelays[1] != 100*time.Millisecond {
		t.Errorf("NextDelay with jv=0.5 = %v, want 100ms", expectedDelays[1])
	}
	if expectedDelays[2] != 110*time.Millisecond {
		t.Errorf("NextDelay with jv=1.0 = %v, want 110ms", expectedDelays[2])
	}
}

func TestExponentialBackoffStrategy_NextDelay_DifferentMultipliers(t *testing.T) {
	tests := []struct {
		multiplier    float64
		attempt       int
		expectedDelay time.Duration
	}{
		{multiplier: 1.5, attempt: 0, expectedDelay: 100 * time.Millisecond},  // 100 * 1.5^0 = 100
		{multiplier: 1.5, attempt: 1, expectedDelay: 150 * time.Millisecond},  // 100 * 1.5^1 = 150
		{multiplier: 1.5, attempt: 2, expectedDelay: 225 * time.Millisecond},  // 100 * 1.5^2 = 225
		{multiplier: 3.0, attempt: 0, expectedDelay: 100 * time.Millisecond},  // 100 * 3^0 = 100
		{multiplier: 3.0, attempt: 1, expectedDelay: 300 * time.Millisecond},  // 100 * 3^1 = 300
		{multiplier: 3.0, attempt: 2, expectedDelay: 900 * time.Millisecond},  // 100 * 3^2 = 900
	}

	for _, tt := range tests {
		strategy := NewExponentialBackoff(5,
			WithInitialDelay(100*time.Millisecond),
			WithMultiplier(tt.multiplier),
			WithJitter(0),
		)

		delay := strategy.NextDelay(tt.attempt)
		if delay != tt.expectedDelay {
			t.Errorf("NextDelay(attempt=%d, multiplier=%v) = %v, want %v",
				tt.attempt, tt.multiplier, delay, tt.expectedDelay)
		}
	}
}

func TestExponentialBackoffStrategy_Chaining(t *testing.T) {
	// Test functional options pattern
	strategy := NewExponentialBackoff(3,
		WithInitialDelay(50*time.Millisecond),
		WithMaxDelay(5*time.Second),
		WithMultiplier(3.0),
		WithJitter(0.2),
	)

	if strategy.InitialDelay() != 50*time.Millisecond {
		t.Errorf("Chained InitialDelay incorrect")
	}
	if strategy.MaxDelay() != 5*time.Second {
		t.Errorf("Chained MaxDelay incorrect")
	}
	if strategy.Multiplier() != 3.0 {
		t.Errorf("Chained Multiplier incorrect")
	}
	if strategy.Jitter() != 0.2 {
		t.Errorf("Chained Jitter incorrect")
	}
	if strategy.MaxAttempts() != 3 {
		t.Errorf("Chained MaxAttempts incorrect")
	}
}

func TestExponentialBackoffStrategy_MaxAttempts_Variations(t *testing.T) {
	tests := []struct {
		maxAttempts int
	}{
		{maxAttempts: 0},  // No retries
		{maxAttempts: 1},  // One retry
		{maxAttempts: 5},  // Five retries
		{maxAttempts: -1}, // Unlimited retries
	}

	for _, tt := range tests {
		strategy := NewExponentialBackoff(tt.maxAttempts)
		if strategy.MaxAttempts() != tt.maxAttempts {
			t.Errorf("MaxAttempts() = %d, want %d", strategy.MaxAttempts(), tt.maxAttempts)
		}
	}
}

func TestExponentialBackoffStrategy_RealWorldScenario(t *testing.T) {
	// Simulate a realistic retry scenario:
	// - Start with 200ms delay
	// - Double each time
	// - Cap at 10 seconds
	// - Allow up to 5 retries

	strategy := NewExponentialBackoff(5,
		WithInitialDelay(200*time.Millisecond),
		WithMultiplier(2.0),
		WithMaxDelay(10*time.Second),
		WithJitter(0), // Disable for predictable test
	)

	expectedSequence := []time.Duration{
		200 * time.Millisecond,  // Attempt 0
		400 * time.Millisecond,  // Attempt 1
		800 * time.Millisecond,  // Attempt 2
		1600 * time.Millisecond, // Attempt 3
		3200 * time.Millisecond, // Attempt 4
	}

	for attempt, expected := range expectedSequence {
		delay := strategy.NextDelay(attempt)
		if delay != expected {
			t.Errorf("Attempt %d: delay = %v, want %v", attempt, delay, expected)
		}
	}

	// Total time if all retries are exhausted: 200+400+800+1600+3200 = 6200ms = 6.2s
	totalDelay := time.Duration(0)
	for i := 0; i < 5; i++ {
		totalDelay += strategy.NextDelay(i)
	}
	expectedTotal := 6200 * time.Millisecond
	if totalDelay != expectedTotal {
		t.Errorf("Total delay = %v, want %v", totalDelay, expectedTotal)
	}
}
