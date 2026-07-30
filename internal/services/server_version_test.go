package services

import (
	"errors"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

// A server below the floor must be rejected with a pgmi diagnostic naming the
// requirement, classified as a configuration error so it exits 10 — not with
// whatever the first unsupported construct happens to raise. Before this guard
// a PostgreSQL 10 server answered "function hashtextextended does not exist",
// which names neither pgmi nor the version it needs.
func TestVerifyServerVersion(t *testing.T) {
	tests := []struct {
		name        string
		versionNum  int
		versionText string
		wantErr     bool
	}{
		{"below the floor", 100023, "10.23", true},
		{"one release below", 90624, "9.6.24", true},
		{"exactly the floor", 110000, "11.0", false},
		{"the floor, patched", 110016, "11.16 (Debian 11.16-1.pgdg90+1)", false},
		{"current release", 170010, "17.10", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyServerVersion(tt.versionNum, tt.versionText)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("PostgreSQL %s is supported but was rejected: %v", tt.versionText, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("PostgreSQL %s is below the floor but was accepted", tt.versionText)
			}
			// Exit 10: rejected before doing work, not a generic failure.
			if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
				t.Errorf("exit code %d, want %d (invalid configuration): %v",
					got, pgmi.ExitConfigError, err)
			}
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Errorf("expected an ErrInvalidConfig chain, got %v", err)
			}
			// The message has to be actionable on its own.
			for _, want := range []string{tt.versionText, "11"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the diagnostic omits %q: %v", want, err)
				}
			}
		})
	}
}
