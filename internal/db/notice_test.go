package db

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfigurePool_PassesUnlocalizedSeverity(t *testing.T) {
	var got string
	poolConfig, err := pgxpool.ParseConfig("host=localhost")
	if err != nil {
		t.Fatal(err)
	}
	configurePool(poolConfig, Output{Notice: func(severity, _, _, _ string) { got = severity }})

	poolConfig.ConnConfig.OnNotice(nil, &pgconn.Notice{Severity: "WARNUNG", SeverityUnlocalized: "WARNING", Message: "m"})

	if got != "WARNING" {
		t.Errorf("severity = %q, want WARNING", got)
	}
}

func TestConfigurePool_WithoutNoticeHandlerDiscards(t *testing.T) {
	poolConfig, err := pgxpool.ParseConfig("host=localhost")
	if err != nil {
		t.Fatal(err)
	}
	configurePool(poolConfig, Output{})
	poolConfig.ConnConfig.OnNotice(nil, &pgconn.Notice{SeverityUnlocalized: "NOTICE", Message: "m"})
}

func TestShouldForward(t *testing.T) {
	tests := []struct {
		name     string
		severity string
		routine  string
		want     bool
	}{
		{"raise debug kept", "DEBUG", "exec_stmt_raise", true},
		{"server debug dropped", "DEBUG", "index_build", false},
		{"debug without routine dropped", "DEBUG", "", false},
		{"notice kept", "NOTICE", "index_build", true},
		{"warning kept", "WARNING", "", true},
		{"info kept", "INFO", "exec_stmt_raise", true},
		{"log kept", "LOG", "errfinish", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &pgconn.Notice{SeverityUnlocalized: tt.severity, Routine: tt.routine}
			if got := shouldForward(n); got != tt.want {
				t.Errorf("shouldForward(%s, %s) = %v, want %v", tt.severity, tt.routine, got, tt.want)
			}
		})
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
