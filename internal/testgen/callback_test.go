package testgen

import (
	"strings"
	"testing"
)

func ptr(s string) *string {
	return &s
}

func TestFormatCallbackInvocation(t *testing.T) {
	tests := []struct {
		name     string
		callback string
		event    string
		path     *string
		dir      string
		depth    int
		ordinal  int
		wantPat  string
	}{
		{
			name:     "fixture_start with path",
			callback: "pg_temp.cb",
			event:    EventFixtureStart,
			path:     ptr("./t/_setup.sql"),
			dir:      "./t/",
			depth:    0,
			ordinal:  1,
			wantPat:  `SELECT pg_temp.cb(ROW('fixture_start', './t/_setup.sql', './t/', 0, 1, NULL)::pg_temp.pgmi_test_event);`,
		},
		{
			name:     "suite_start without path",
			callback: "pg_temp.cb",
			event:    EventSuiteStart,
			path:     nil,
			dir:      "",
			depth:    0,
			ordinal:  0,
			wantPat:  `SELECT pg_temp.cb(ROW('suite_start', NULL, '', 0, 0, NULL)::pg_temp.pgmi_test_event);`,
		},
		{
			name:     "test_end with path",
			callback: "myschema.reporter",
			event:    EventTestEnd,
			path:     ptr("./api/__test__/users.sql"),
			dir:      "./api/__test__/",
			depth:    1,
			ordinal:  5,
			wantPat:  `SELECT myschema.reporter(ROW('test_end', './api/__test__/users.sql', './api/__test__/', 1, 5, NULL)::pg_temp.pgmi_test_event);`,
		},
		{
			name:     "path with single quote",
			callback: "pg_temp.cb",
			event:    EventTestStart,
			path:     ptr("./test's/file.sql"),
			dir:      "./test's/",
			depth:    0,
			ordinal:  1,
			wantPat:  `SELECT pg_temp.cb(ROW('test_start', './test''s/file.sql', './test''s/', 0, 1, NULL)::pg_temp.pgmi_test_event);`,
		},
		{
			name:     "rollback event",
			callback: "pg_temp.observer",
			event:    EventRollback,
			path:     ptr("./t/test.sql"),
			dir:      "./t/",
			depth:    0,
			ordinal:  3,
			wantPat:  `SELECT pg_temp.observer(ROW('rollback', './t/test.sql', './t/', 0, 3, NULL)::pg_temp.pgmi_test_event);`,
		},
		{
			name:     "teardown_start without path",
			callback: "pg_temp.cb",
			event:    EventTeardownStart,
			path:     nil,
			dir:      "./t/",
			depth:    0,
			ordinal:  4,
			wantPat:  `SELECT pg_temp.cb(ROW('teardown_start', NULL, './t/', 0, 4, NULL)::pg_temp.pgmi_test_event);`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCallbackInvocation(tt.callback, tt.event, tt.path, tt.dir, tt.depth, tt.ordinal)
			if got != tt.wantPat {
				t.Errorf("FormatCallbackInvocation() =\n%s\nwant:\n%s", got, tt.wantPat)
			}
		})
	}
}

func TestEventConstants(t *testing.T) {
	events := []string{
		EventSuiteStart, EventSuiteEnd,
		EventFixtureStart, EventFixtureEnd,
		EventTestStart, EventTestEnd,
		EventRollback, EventTeardownStart, EventTeardownEnd,
	}

	// Verify non-empty
	for _, e := range events {
		if e == "" {
			t.Error("Event constant should not be empty")
		}
	}

	// Verify unique
	seen := make(map[string]bool)
	for _, e := range events {
		if seen[e] {
			t.Errorf("Duplicate event constant: %s", e)
		}
		seen[e] = true
	}

	// Verify expected values
	expected := map[string]string{
		"suite_start":    EventSuiteStart,
		"suite_end":      EventSuiteEnd,
		"fixture_start":  EventFixtureStart,
		"fixture_end":    EventFixtureEnd,
		"test_start":     EventTestStart,
		"test_end":       EventTestEnd,
		"rollback":       EventRollback,
		"teardown_start": EventTeardownStart,
		"teardown_end":   EventTeardownEnd,
	}

	for val, constant := range expected {
		if constant != val {
			t.Errorf("Event constant %s should have value %q", constant, val)
		}
	}
}

func TestFormatCallbackInvocation_ValidSQL(t *testing.T) {
	result := FormatCallbackInvocation("pg_temp.cb", EventSuiteStart, nil, "", 0, 0)

	// Should start with SELECT and end with semicolon
	if !strings.HasPrefix(result, "SELECT ") {
		t.Error("Should start with SELECT")
	}
	if !strings.HasSuffix(result, ";") {
		t.Error("Should end with semicolon")
	}
	if !strings.Contains(result, "::pg_temp.pgmi_test_event") {
		t.Error("Should cast to pgmi_test_event type")
	}
}

func TestFormatCallbackExistenceCheck(t *testing.T) {
	tests := []struct {
		name     string
		callback string
		wantSQL  string
	}{
		{
			name:     "simple callback",
			callback: "pg_temp.my_callback",
			wantSQL:  `DO $$ BEGIN PERFORM 'pg_temp.my_callback'::regproc; EXCEPTION WHEN undefined_function THEN RAISE EXCEPTION 'Callback function "pg_temp.my_callback" does not exist. Expected signature: (pg_temp.pgmi_test_event) RETURNS void'; END $$;`,
		},
		{
			name:     "callback with single quote in name",
			callback: "pg_temp.cb's",
			wantSQL:  `DO $$ BEGIN PERFORM 'pg_temp.cb''s'::regproc; EXCEPTION WHEN undefined_function THEN RAISE EXCEPTION 'Callback function "pg_temp.cb''s" does not exist. Expected signature: (pg_temp.pgmi_test_event) RETURNS void'; END $$;`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCallbackExistenceCheck(tt.callback)
			if got != tt.wantSQL {
				t.Errorf("FormatCallbackExistenceCheck() =\n%s\nwant:\n%s", got, tt.wantSQL)
			}
		})
	}
}

func TestFormatCallbackExistenceCheck_Structure(t *testing.T) {
	result := FormatCallbackExistenceCheck("pg_temp.test_cb")

	if !strings.HasPrefix(result, "DO $$") {
		t.Error("Should start with DO $$")
	}
	if !strings.HasSuffix(result, "END $$;") {
		t.Error("Should end with END $$;")
	}
	if !strings.Contains(result, "::regproc") {
		t.Error("Should use regproc cast for function lookup")
	}
	if !strings.Contains(result, "EXCEPTION WHEN undefined_function") {
		t.Error("Should handle undefined_function exception")
	}
	if !strings.Contains(result, "Expected signature:") {
		t.Error("Should include expected signature in error message")
	}
}
