package retry

import (
	"testing"
	"time"
)

func TestExponentialBackoff_Defaults(t *testing.T) {
	b := NewExponentialBackoff(3)
	if b.retries != 3 || b.initialDelay != 100*time.Millisecond || b.maxDelay != 30*time.Second || b.jitter != 0.1 {
		t.Errorf("defaults = %+v", *b)
	}
}

func TestExponentialBackoff_DoublesUpToCap(t *testing.T) {
	b := NewExponentialBackoff(100,
		WithInitialDelay(100*time.Millisecond),
		WithMaxDelay(time.Minute),
		WithJitter(0),
	)
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}
	for attempt, w := range want {
		if got := b.NextDelay(attempt); got != w {
			t.Errorf("NextDelay(%d) = %v, want %v", attempt, got, w)
		}
	}
	for attempt := 10; attempt <= 100; attempt++ {
		if got := b.NextDelay(attempt); got != time.Minute {
			t.Errorf("NextDelay(%d) = %v, want the %v cap", attempt, got, time.Minute)
		}
	}
}

func TestExponentialBackoff_JitterStaysInBand(t *testing.T) {
	b := NewExponentialBackoff(3, WithInitialDelay(time.Second), WithJitter(0.2))
	lo, hi := 800*time.Millisecond, 1200*time.Millisecond
	seen := map[time.Duration]bool{}
	for range 200 {
		d := b.NextDelay(0)
		if d < lo || d > hi {
			t.Fatalf("NextDelay(0) = %v, want within [%v, %v]", d, lo, hi)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Error("jitter produced a single value over 200 draws")
	}
}
