package testing

import (
	"regexp"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestEvent represents a parsed test lifecycle event from NOTICE messages.
type TestEvent struct {
	Event     string // suite_start, fixture_start, etc.
	Path      string // Script path (empty for suite/teardown)
	Directory string // e.g., ./__test__/child/
	Depth     int
	Ordinal   int
}

// NoticeCapture collects and parses test events from PostgreSQL NOTICE messages.
// Thread-safe for concurrent use.
type NoticeCapture struct {
	events  []TestEvent
	raw     []string
	pattern *regexp.Regexp
	mu      sync.Mutex
}

// NewNoticeCapture creates a new NoticeCapture instance.
// Looks for NOTICE messages matching: [TRACE]event|path|dir|depth|ordinal
func NewNoticeCapture() *NoticeCapture {
	return &NoticeCapture{
		events:  make([]TestEvent, 0),
		raw:     make([]string, 0),
		pattern: regexp.MustCompile(`^\[TRACE\]([^|]+)\|([^|]*)\|([^|]*)\|(\d+)\|(\d+)$`),
	}
}

// Handler returns a function suitable for pgx's OnNotice callback.
// The handler parses [TRACE] messages and stores them as TestEvents.
func (nc *NoticeCapture) Handler() func(*pgconn.PgConn, *pgconn.Notice) {
	return func(_ *pgconn.PgConn, n *pgconn.Notice) {
		if n == nil {
			return
		}

		nc.mu.Lock()
		defer nc.mu.Unlock()

		nc.raw = append(nc.raw, n.Message)

		match := nc.pattern.FindStringSubmatch(n.Message)
		if match == nil {
			return
		}

		depth, _ := strconv.Atoi(match[4])
		ordinal, _ := strconv.Atoi(match[5])

		nc.events = append(nc.events, TestEvent{
			Event:     match[1],
			Path:      match[2],
			Directory: match[3],
			Depth:     depth,
			Ordinal:   ordinal,
		})
	}
}

// Events returns a copy of all captured test events.
func (nc *NoticeCapture) Events() []TestEvent {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	result := make([]TestEvent, len(nc.events))
	copy(result, nc.events)
	return result
}

// EventSequence returns just the event names in order.
func (nc *NoticeCapture) EventSequence() []string {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	result := make([]string, len(nc.events))
	for i, e := range nc.events {
		result[i] = e.Event
	}
	return result
}

// FindEvents returns all events matching the specified event type.
func (nc *NoticeCapture) FindEvents(eventType string) []TestEvent {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	var result []TestEvent
	for _, e := range nc.events {
		if e.Event == eventType {
			result = append(result, e)
		}
	}
	return result
}

// RawNotices returns all raw NOTICE messages received.
func (nc *NoticeCapture) RawNotices() []string {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	result := make([]string, len(nc.raw))
	copy(result, nc.raw)
	return result
}

// Reset clears all captured events and raw notices.
func (nc *NoticeCapture) Reset() {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	nc.events = make([]TestEvent, 0)
	nc.raw = make([]string, 0)
}

// Count returns the number of captured events.
func (nc *NoticeCapture) Count() int {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	return len(nc.events)
}
