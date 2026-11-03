package db

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestForwardNotice_PassesUnlocalizedSeverity(t *testing.T) {
	var got string
	orig := NoticeHandler
	NoticeHandler = func(severity, _, _, _ string) { got = severity }
	t.Cleanup(func() { NoticeHandler = orig })

	forwardNotice(nil, &pgconn.Notice{Severity: "WARNUNG", SeverityUnlocalized: "WARNING", Message: "m"})

	if got != "WARNING" {
		t.Errorf("severity = %q, want WARNING", got)
	}
}

func TestWriteNotice(t *testing.T) {
	tests := []struct {
		name                                    string
		prefix, severity, message, detail, hint string
		want                                    string
	}{
		{"notice unprefixed", "", "NOTICE", "applying", "", "", "applying\n"},
		{"info unprefixed", "", "INFO", "applying", "", "", "applying\n"},
		{"warning labelled", "", "WARNING", "this is a warning", "", "", "WARNING:  this is a warning\n"},
		{"detail and hint two spaces", "", "NOTICE", "m", "d", "h", "m\nDETAIL:  d\nHINT:  h\n"},
		{"line prefix on every line", "[0.10s] ", "WARNING", "m", "d", "h",
			"[0.10s] WARNING:  m\n[0.10s] DETAIL:  d\n[0.10s] HINT:  h\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			WriteNotice(&b, tt.prefix, tt.severity, tt.message, tt.detail, tt.hint)
			if b.String() != tt.want {
				t.Errorf("got %q, want %q", b.String(), tt.want)
			}
		})
	}
}
