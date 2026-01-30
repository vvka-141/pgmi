package pgmi_test

import (
	"errors"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestExitCodeForError_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"unknown flag", errors.New("unknown flag --foo"), pgmi.ExitUsageError},
		{"unknown shorthand flag", errors.New("unknown shorthand flag: 'x'"), pgmi.ExitUsageError},
		{"accepts args", errors.New("accepts 1 arg(s), received 0"), pgmi.ExitUsageError},
		{"required flag", errors.New("required flag \"database\" not set"), pgmi.ExitUsageError},
		{"invalid argument", errors.New("invalid argument \"abc\" for \"--port\""), pgmi.ExitUsageError},
		{"general error", errors.New("something went wrong"), pgmi.ExitGeneralError},
		{"nil error", nil, pgmi.ExitSuccess},
		{"connection failed", pgmi.ErrConnectionFailed, pgmi.ExitConnectionError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pgmi.ExitCodeForError(tt.err); got != tt.want {
				t.Errorf("ExitCodeForError(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
