package retry

import (
	"math"
	"math/rand"
	"time"
)

// ExponentialBackoff implements exponential backoff with jitter.
type ExponentialBackoff struct {
	// initialDelay is the delay for the first retry attempt
	initialDelay time.Duration

	// maxDelay is the maximum delay between attempts
	maxDelay time.Duration

	// multiplier is the factor by which delay increases (typically 2.0)
	multiplier float64

	// maxAttempts is the maximum number of retry attempts (-1 = unlimited, 0 = no retries)
	maxAttempts int

	// jitter adds randomness to prevent thundering herd (0.0-1.0, typically 0.1)
	// Jitter of 0.1 means +/- 10% randomness
	jitter float64

	// jitterFunc provides random values [0, 1) for jitter calculation (defaults to math.random equivalent)
	jitterFunc func() float64
}

// BackoffOption is a functional option for configuring ExponentialBackoff.
type BackoffOption func(*ExponentialBackoff)

// WithInitialDelay sets the initial delay for the first retry attempt.
func WithInitialDelay(d time.Duration) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.initialDelay = d
	}
}

// WithMaxDelay sets the maximum delay between retry attempts.
func WithMaxDelay(d time.Duration) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.maxDelay = d
	}
}

// WithMultiplier sets the factor by which delay increases between attempts.
func WithMultiplier(m float64) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.multiplier = m
	}
}

// WithJitter sets the jitter factor (0.0-1.0) to add randomness to delays.
func WithJitter(j float64) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.jitter = j
	}
}

// WithJitterFunc sets a custom function for generating random jitter values.
func WithJitterFunc(f func() float64) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.jitterFunc = f
	}
}

// NewExponentialBackoff creates a new exponential backoff strategy with sensible defaults.
// Additional configuration can be provided via functional options.
//
// Example:
//
//	backoff := retry.NewExponentialBackoff(3,
//	    retry.WithInitialDelay(200 * time.Millisecond),
//	    retry.WithMaxDelay(1 * time.Minute),
//	    retry.WithJitter(0.2),
//	)
func NewExponentialBackoff(maxAttempts int, opts ...BackoffOption) *ExponentialBackoff {
	b := &ExponentialBackoff{
		initialDelay: 100 * time.Millisecond,
		maxDelay:     30 * time.Second,
		multiplier:   2.0,
		maxAttempts:  maxAttempts,
		jitter:       0.1,
		jitterFunc:   nil, // Will use default in NextDelay
	}

	// Apply options
	for _, opt := range opts {
		opt(b)
	}

	return b
}

// NextDelay calculates the delay for the given attempt using exponential backoff.
func (b *ExponentialBackoff) NextDelay(attempt int) time.Duration {
	// Calculate base delay: initialDelay * (multiplier ^ attempt)
	exponent := float64(attempt)
	delayMs := float64(b.initialDelay.Milliseconds()) * math.Pow(b.multiplier, exponent)

	// Cap at maxDelay
	if delayMs > float64(b.maxDelay.Milliseconds()) {
		delayMs = float64(b.maxDelay.Milliseconds())
	}

	// Apply jitter to prevent thundering herd
	if b.jitter > 0 {
		jitterFunc := b.jitterFunc
		if jitterFunc == nil {
			// Default: real randomness for production use.
			// Tests should explicitly set jitterFunc to a deterministic function.
			jitterFunc = rand.Float64
		}

		// Apply jitter: delay * (1 +/- jitter * random)
		// Example: jitter=0.1, random=0.7 => delay * (1 + 0.1 * (0.7 - 0.5) * 2) = delay * 1.02
		randomOffset := (jitterFunc() - 0.5) * 2.0 // Map [0,1) to [-1,1)
		jitterFactor := 1.0 + (b.jitter * randomOffset)
		delayMs *= jitterFactor
	}

	return time.Duration(delayMs) * time.Millisecond
}

// MaxAttempts returns the maximum number of retry attempts.
func (b *ExponentialBackoff) MaxAttempts() int {
	return b.maxAttempts
}

// InitialDelay returns the initial delay for tests and debugging.
func (b *ExponentialBackoff) InitialDelay() time.Duration {
	return b.initialDelay
}

// MaxDelay returns the maximum delay for tests and debugging.
func (b *ExponentialBackoff) MaxDelay() time.Duration {
	return b.maxDelay
}

// Multiplier returns the backoff multiplier for tests and debugging.
func (b *ExponentialBackoff) Multiplier() float64 {
	return b.multiplier
}

// Jitter returns the jitter factor for tests and debugging.
func (b *ExponentialBackoff) Jitter() float64 {
	return b.jitter
}
