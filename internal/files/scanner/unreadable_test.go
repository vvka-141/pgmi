package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/checksum"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestScanDirectory_UnloadableProjectPathIsInvalidConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks and POSIX permissions")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads files without read permission")
	}

	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		path  string
	}{
		{"symlink", func(t *testing.T, dir string) {
			if err := os.Symlink(filepath.Join(dir, "deploy.sql"), filepath.Join(dir, "link.sql")); err != nil {
				t.Fatal(err)
			}
		}, "link.sql"},
		{"file without read permission", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "secret.sql")
			if err := os.WriteFile(p, []byte("SELECT 1;"), 0o000); err != nil {
				t.Fatal(err)
			}
		}, "secret.sql"},
		{"directory without read permission", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "locked")
			if err := os.Mkdir(p, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
		}, "locked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "deploy.sql"), []byte("SELECT 1;"), 0o644); err != nil {
				t.Fatal(err)
			}
			tt.setup(t, dir)

			_, err := NewScanner(checksum.New()).ScanDirectory(dir)
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Fatalf("want ErrInvalidConfig (exit 10), got: %v", err)
			}
			for _, want := range []string{tt.path, ".pgmiignore"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error should mention %q, got: %v", want, err)
				}
			}
		})
	}
}
