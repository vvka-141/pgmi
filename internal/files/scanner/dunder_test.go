package scanner

import (
	"errors"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestValidateDunderDirectories(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"./migrations/__test__/test_foo.sql", false},
		{"./migrations/__tests__/test_foo.sql", false},
		{"./migrations/schema.sql", false},
		{"./migrations/__foo__/bar.sql", true},
		{"./migrations/__bar__/x.sql", true},
		{"./test/test.sql", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validateDunderDirectories(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDunderDirectories(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

// A misspelled dunder directory is a project error caught before pgmi connects,
// exactly like a path that does not exist, a path that is a file, or a
// duplicate <pgmi-meta id> — all of which exit 10. This one exited 1, which
// tells a script nothing. Confirmed against the binary: `__tets__` now exits 10
// while a duplicate id still does and a valid __tests__ project still exits 0.
func TestValidateDunderDirectories_UnsupportedIsInvalidConfig(t *testing.T) {
	err := validateDunderDirectories("./__tets__/test_x.sql")
	if err == nil {
		t.Fatal("a misspelled dunder directory must be rejected")
	}
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Errorf("not an ErrInvalidConfig chain, so this exits 1 rather than 10: %v", err)
	}
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Errorf("exit code %d, want %d", got, pgmi.ExitConfigError)
	}
	// The message still has to name the offender and the allowed spellings.
	for _, want := range []string{"__tets__", "__test__", "__tests__"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message no longer mentions %q: %v", want, err)
		}
	}
}
