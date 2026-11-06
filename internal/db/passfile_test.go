package db

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIgnoreExposedPassfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("libpq makes no permission check on Windows")
	}
	t.Setenv("PGPASSWORD", "")
	path := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(path, []byte("*:*:*:*:fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", path)

	for _, tt := range []struct {
		name        string
		mode        os.FileMode
		own         string
		want        string
		wantWarning bool
	}{
		{"a private file is used", 0o600, "", "fromfile", false},
		{"a world-readable file is ignored", 0o644, "", "", true},
		{"a group-readable file is ignored", 0o640, "", "", true},
		{"a password pgmi was given is never touched", 0o644, "given", "fromfile", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			passfileWarning = sync.Once{}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatal(err)
			}
			cfg := &pgconn.Config{Password: "fromfile"}
			var warn strings.Builder
			IgnoreExposedPassfile(cfg, tt.own, "", func(format string, args ...any) { fmt.Fprintf(&warn, format, args...) })
			if cfg.Password != tt.want {
				t.Errorf("password %q, want %q", cfg.Password, tt.want)
			}
			if got := strings.Contains(warn.String(), "has group or world access"); got != tt.wantWarning {
				t.Errorf("warning %q, want one: %v", warn.String(), tt.wantWarning)
			}
		})
	}
}

func TestIgnoreExposedPassfile_TheConnectionsPassfileWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("libpq makes no permission check on Windows")
	}
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGPASSFILE", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	write := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte("*:*:*:*:fromfile\n"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".pgpass"), 0o644)
	private := filepath.Join(t.TempDir(), "private")
	write(private, 0o600)
	exposed := filepath.Join(t.TempDir(), "exposed")
	write(exposed, 0o644)

	for _, tt := range []struct {
		name, passfile, want, warnsAbout string
	}{
		{"an exposed passfile= is ignored", exposed, "", exposed},
		{"a private passfile= is used whatever ~/.pgpass is", private, "fromfile", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			passfileWarning = sync.Once{}
			cfg := &pgconn.Config{Password: "fromfile"}
			var warn strings.Builder
			IgnoreExposedPassfile(cfg, "", tt.passfile, func(format string, args ...any) { fmt.Fprintf(&warn, format, args...) })
			if cfg.Password != tt.want {
				t.Errorf("password %q, want %q", cfg.Password, tt.want)
			}
			if tt.warnsAbout == "" && warn.Len() > 0 || tt.warnsAbout != "" && !strings.Contains(warn.String(), tt.warnsAbout) {
				t.Errorf("warning %q, want one about %q", warn.String(), tt.warnsAbout)
			}
		})
	}
}

func TestParseConnectionString_KeepsPassfile(t *testing.T) {
	for _, connStr := range []string{
		"postgresql://u@h/d?passfile=/secrets/pgpass",
		"host=h user=u dbname=d passfile=/secrets/pgpass",
	} {
		cfg, err := ParseConnectionString(connStr)
		if err != nil {
			t.Fatalf("%s: %v", connStr, err)
		}
		if got := cfg.AdditionalParams["passfile"]; got != "/secrets/pgpass" {
			t.Errorf("%s: passfile = %q, want /secrets/pgpass", connStr, got)
		}
	}
}
