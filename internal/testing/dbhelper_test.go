package testing

import (
	"errors"
	"strings"
	"testing"
)

// TestResolveTestConnection_RequireTurnsSkipIntoFailure covers the gate that
// makes every other integration test trustworthy: without PGMI_REQUIRE_DB an
// unreachable Docker daemon silently skips the whole suite and CI reports green.
func TestResolveTestConnection_RequireTurnsSkipIntoFailure(t *testing.T) {
	cause := errors.New("docker daemon not running")
	broken := func() (string, error) { return "", cause }
	working := func() (string, error) { return "postgres://container", nil }

	tests := []struct {
		name      string
		connEnv   string
		require   string
		start     func() (string, error)
		wantConn  string
		wantMsg   bool
		wantFatal bool
	}{
		{name: "PGMI_TEST_CONN wins and never starts a container", connEnv: "postgres://explicit", require: "1", start: broken, wantConn: "postgres://explicit"},
		{name: "container started", start: working, wantConn: "postgres://container"},
		{name: "unavailable, not required", start: broken, wantMsg: true},
		{name: "unavailable, required=1", require: "1", start: broken, wantMsg: true, wantFatal: true},
		{name: "unavailable, required=true", require: "true", start: broken, wantMsg: true, wantFatal: true},
		{name: "unavailable, required=TRUE", require: "TRUE", start: broken, wantMsg: true, wantFatal: true},
		{name: "unavailable, required=0", require: "0", start: broken, wantMsg: true},
		{name: "unavailable, required=false", require: "false", start: broken, wantMsg: true},
		{name: "unavailable, required blank", require: "  ", start: broken, wantMsg: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, msg, fatal := resolveTestConnection(tt.connEnv, tt.require, tt.start)

			if conn != tt.wantConn {
				t.Errorf("conn = %q, want %q", conn, tt.wantConn)
			}
			if (msg != "") != tt.wantMsg {
				t.Errorf("msg = %q, want non-empty: %v", msg, tt.wantMsg)
			}
			if fatal != tt.wantFatal {
				t.Errorf("fatal = %v, want %v", fatal, tt.wantFatal)
			}
			if tt.wantMsg && !strings.Contains(msg, cause.Error()) {
				t.Errorf("message %q drops the underlying cause", msg)
			}
		})
	}
}
