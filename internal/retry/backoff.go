package retry

import (
	"math"
	"math/rand"
	"time"
)

// ExponentialBackoff doubles the delay after each retry, up to a cap, with jitter.
type ExponentialBackoff struct {
	// retries is how many attempts follow the first one.
	retries      int
	initialDelay time.Duration
	maxDelay     time.Duration
	// jitter adds +/- that fraction of randomness so clients do not retry in lockstep.
	jitter float64
}

// BackoffOption is a functional option for configuring ExponentialBackoff.
type BackoffOption func(*ExponentialBackoff)

// WithInitialDelay sets the delay before the first retry.
func WithInitialDelay(d time.Duration) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.initialDelay = d
	}
}

// WithMaxDelay caps the delay between retries.
func WithMaxDelay(d time.Duration) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.maxDelay = d
	}
}

// WithJitter sets the jitter fraction (0.0-1.0).
func WithJitter(j float64) BackoffOption {
	return func(b *ExponentialBackoff) {
		b.jitter = j
	}
}

// NewExponentialBackoff allows retries attempts after the first, starting at
// 100ms and capped at 30s unless options say otherwise.
func NewExponentialBackoff(retries int, opts ...BackoffOption) *ExponentialBackoff {
	b := &ExponentialBackoff{
		retries:      retries,
		initialDelay: 100 * time.Millisecond,
		maxDelay:     30 * time.Second,
		jitter:       0.1,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// NextDelay returns the wait before retry number attempt (zero-indexed).
func (b *ExponentialBackoff) NextDelay(attempt int) time.Duration {
	delayMs := float64(b.initialDelay.Milliseconds()) * math.Pow(2, float64(attempt))
	delayMs = min(delayMs, float64(b.maxDelay.Milliseconds()))
	if b.jitter > 0 {
		delayMs *= 1 + b.jitter*(rand.Float64()-0.5)*2
	}
	return time.Duration(delayMs) * time.Millisecond
}
