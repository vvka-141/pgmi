package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
)

// %q escapes the separators in a Windows path, so `pgmi deploy C:\proj\app`
// answered with C:\\proj\\app — a path the user cannot paste back into a
// shell, in exactly the messages that exist to tell them which path to fix.
func TestScannerErrorsQuotePathsWithoutEscaping(t *testing.T) {
	s := NewScanner(checksum.New())

	t.Run("missing project path", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-project")
		err := s.ValidateDeploySQL(missing)
		if err == nil {
			t.Fatal("expected an error for a missing project path")
		}
		assertPathReadable(t, err.Error(), missing)
	})

	t.Run("project path is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		err := s.ValidateDeploySQL(file)
		if err == nil {
			t.Fatal("expected an error when the project path is a file")
		}
		assertPathReadable(t, err.Error(), file)
	})
}

func assertPathReadable(t *testing.T, msg, path string) {
	t.Helper()
	if !strings.Contains(msg, path) {
		t.Errorf("message does not carry the path verbatim:\n  message: %s\n  path:    %s", msg, path)
	}
	if strings.Contains(msg, `\\`) {
		t.Errorf("message escapes path separators (%%q instead of \"%%s\"):\n  %s", msg)
	}
}
