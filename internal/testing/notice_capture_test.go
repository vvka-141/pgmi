package testing

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNoticeCapture_ParsesTraceFormat(t *testing.T) {
	nc := NewNoticeCapture()
	handler := nc.Handler()

	// Simulate NOTICE messages in trace format
	testCases := []struct {
		message  string
		expected *TestEvent
	}{
		{
			message: "[TRACE]suite_start||./__test__/|0|0",
			expected: &TestEvent{
				Event:     "suite_start",
				Path:      "",
				Directory: "./__test__/",
				Depth:     0,
				Ordinal:   0,
			},
		},
		{
			message: "[TRACE]fixture_start|./__test__/_setup.sql|./__test__/|0|1",
			expected: &TestEvent{
				Event:     "fixture_start",
				Path:      "./__test__/_setup.sql",
				Directory: "./__test__/",
				Depth:     0,
				Ordinal:   1,
			},
		},
		{
			message: "[TRACE]test_start|./__test__/test_foo.sql|./__test__/|0|3",
			expected: &TestEvent{
				Event:     "test_start",
				Path:      "./__test__/test_foo.sql",
				Directory: "./__test__/",
				Depth:     0,
				Ordinal:   3,
			},
		},
		{
			message: "[TRACE]teardown_end||./__test__/child/|1|10",
			expected: &TestEvent{
				Event:     "teardown_end",
				Path:      "",
				Directory: "./__test__/child/",
				Depth:     1,
				Ordinal:   10,
			},
		},
		{
			// Non-trace message should not be parsed as event
			message:  "INFO: Some other notice",
			expected: nil,
		},
	}

	for _, tc := range testCases {
		nc.Reset()
		handler(nil, &pgconn.Notice{Message: tc.message})

		events := nc.Events()

		if tc.expected == nil {
			if len(events) != 0 {
				t.Errorf("Message %q should not produce events, got %d", tc.message, len(events))
			}
			continue
		}

		if len(events) != 1 {
			t.Errorf("Message %q should produce 1 event, got %d", tc.message, len(events))
			continue
		}

		e := events[0]
		if e.Event != tc.expected.Event {
			t.Errorf("Event mismatch for %q: got %q, want %q", tc.message, e.Event, tc.expected.Event)
		}
		if e.Path != tc.expected.Path {
			t.Errorf("Path mismatch for %q: got %q, want %q", tc.message, e.Path, tc.expected.Path)
		}
		if e.Directory != tc.expected.Directory {
			t.Errorf("Directory mismatch for %q: got %q, want %q", tc.message, e.Directory, tc.expected.Directory)
		}
		if e.Depth != tc.expected.Depth {
			t.Errorf("Depth mismatch for %q: got %d, want %d", tc.message, e.Depth, tc.expected.Depth)
		}
		if e.Ordinal != tc.expected.Ordinal {
			t.Errorf("Ordinal mismatch for %q: got %d, want %d", tc.message, e.Ordinal, tc.expected.Ordinal)
		}
	}
}

func TestNoticeCapture_EventSequence(t *testing.T) {
	nc := NewNoticeCapture()
	handler := nc.Handler()

	messages := []string{
		"[TRACE]suite_start|||0|0",
		"[TRACE]fixture_start|./__test__/_setup.sql|./__test__/|0|1",
		"[TRACE]fixture_end|./__test__/_setup.sql|./__test__/|0|1",
		"[TRACE]test_start|./__test__/test_foo.sql|./__test__/|0|2",
		"[TRACE]test_end|./__test__/test_foo.sql|./__test__/|0|2",
		"[TRACE]rollback|./__test__/test_foo.sql|./__test__/|0|2",
		"[TRACE]teardown_start||./__test__/|0|3",
		"[TRACE]teardown_end||./__test__/|0|3",
		"[TRACE]suite_end|||0|3",
	}

	for _, msg := range messages {
		handler(nil, &pgconn.Notice{Message: msg})
	}

	seq := nc.EventSequence()
	expected := []string{
		"suite_start",
		"fixture_start",
		"fixture_end",
		"test_start",
		"test_end",
		"rollback",
		"teardown_start",
		"teardown_end",
		"suite_end",
	}

	if len(seq) != len(expected) {
		t.Fatalf("Sequence length mismatch: got %d, want %d", len(seq), len(expected))
	}

	for i := range expected {
		if seq[i] != expected[i] {
			t.Errorf("Sequence[%d] mismatch: got %q, want %q", i, seq[i], expected[i])
		}
	}
}

func TestNoticeCapture_FindEvents(t *testing.T) {
	nc := NewNoticeCapture()
	handler := nc.Handler()

	messages := []string{
		"[TRACE]suite_start|||0|0",
		"[TRACE]test_start|./__test__/test_a.sql|./__test__/|0|1",
		"[TRACE]test_end|./__test__/test_a.sql|./__test__/|0|1",
		"[TRACE]test_start|./__test__/test_b.sql|./__test__/|0|2",
		"[TRACE]test_end|./__test__/test_b.sql|./__test__/|0|2",
		"[TRACE]suite_end|||0|3",
	}

	for _, msg := range messages {
		handler(nil, &pgconn.Notice{Message: msg})
	}

	tests := nc.FindEvents("test_start")
	if len(tests) != 2 {
		t.Errorf("Expected 2 test_start events, got %d", len(tests))
	}

	suites := nc.FindEvents("suite_start")
	if len(suites) != 1 {
		t.Errorf("Expected 1 suite_start event, got %d", len(suites))
	}

	none := nc.FindEvents("nonexistent")
	if len(none) != 0 {
		t.Errorf("Expected 0 nonexistent events, got %d", len(none))
	}
}

func TestNoticeCapture_RawNotices(t *testing.T) {
	nc := NewNoticeCapture()
	handler := nc.Handler()

	messages := []string{
		"[TRACE]suite_start|||0|0",
		"Some other notice",
		"[TRACE]test_start|path|dir|0|1",
	}

	for _, msg := range messages {
		handler(nil, &pgconn.Notice{Message: msg})
	}

	raw := nc.RawNotices()
	if len(raw) != 3 {
		t.Errorf("Expected 3 raw notices, got %d", len(raw))
	}

	// Events should only include parsed trace messages
	events := nc.Events()
	if len(events) != 2 {
		t.Errorf("Expected 2 parsed events, got %d", len(events))
	}
}

func TestNoticeCapture_Reset(t *testing.T) {
	nc := NewNoticeCapture()
	handler := nc.Handler()

	handler(nil, &pgconn.Notice{Message: "[TRACE]suite_start|||0|0"})

	if nc.Count() != 1 {
		t.Errorf("Expected 1 event before reset, got %d", nc.Count())
	}

	nc.Reset()

	if nc.Count() != 0 {
		t.Errorf("Expected 0 events after reset, got %d", nc.Count())
	}

	if len(nc.RawNotices()) != 0 {
		t.Errorf("Expected 0 raw notices after reset, got %d", len(nc.RawNotices()))
	}
}

func TestNoticeCapture_NilNotice(t *testing.T) {
	nc := NewNoticeCapture()
	handler := nc.Handler()

	// Should not panic on nil notice
	handler(nil, nil)

	if nc.Count() != 0 {
		t.Errorf("Expected 0 events for nil notice, got %d", nc.Count())
	}
}
