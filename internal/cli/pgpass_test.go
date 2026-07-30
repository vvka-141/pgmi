package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestEscapePgpass(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"pass:word", `pass\:word`},
		{`back\slash`, `back\\slash`},
		{`both\:chars`, `both\\\:chars`},
		{"", ""},
		{`\:\`, `\\\:\\`},
		{"multi:colon:password", `multi\:colon\:password`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapePgpass(tt.input)
			if got != tt.want {
				t.Errorf("escapePgpass(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestWritePgpassEntry_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass.conf")
	t.Setenv("PGPASSFILE", path)

	cfg := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "user",
		Password: "secret",
	}

	if err := writePgpassEntry(cfg); err != nil {
		t.Fatalf("writePgpassEntry() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	want := "localhost:5432:testdb:user:secret\n"
	if string(data) != want {
		t.Errorf("file content = %q, want %q", string(data), want)
	}
}

func TestWritePgpassEntry_UpdatesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass.conf")
	t.Setenv("PGPASSFILE", path)

	existing := "otherhost:5432:otherdb:otheruser:oldpass\nlocalhost:5432:testdb:user:oldpass\n"
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "testdb",
		Username: "user",
		Password: "newpass",
	}

	if err := writePgpassEntry(cfg); err != nil {
		t.Fatalf("writePgpassEntry() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(data))
	}
	// The saved entry goes first: libpq stops at the first match.
	if lines[0] != "localhost:5432:testdb:user:newpass" {
		t.Errorf("first line = %q, want the updated entry", lines[0])
	}
	if lines[1] != "otherhost:5432:otherdb:otheruser:oldpass" {
		t.Errorf("unrelated entry lost or modified: %q", lines[1])
	}
}

func TestWritePgpassEntry_NewEntryGoesFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass.conf")
	t.Setenv("PGPASSFILE", path)

	existing := "otherhost:5432:otherdb:otheruser:pass\n"
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &pgmi.ConnectionConfig{
		Host:     "newhost",
		Port:     5433,
		Database: "newdb",
		Username: "newuser",
		Password: "newpass",
	}

	if err := writePgpassEntry(cfg); err != nil {
		t.Fatalf("writePgpassEntry() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(data))
	}
	if lines[0] != "newhost:5433:newdb:newuser:newpass" {
		t.Errorf("first line = %q, want the new entry", lines[0])
	}
	if lines[1] != "otherhost:5432:otherdb:otheruser:pass" {
		t.Errorf("unrelated entry lost or modified: %q", lines[1])
	}
}

func TestWritePgpassEntry_EscapesPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass.conf")
	t.Setenv("PGPASSFILE", path)

	cfg := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "db",
		Username: "user",
		Password: `p:a\ss`,
	}

	if err := writePgpassEntry(cfg); err != nil {
		t.Fatalf("writePgpassEntry() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	want := `localhost:5432:db:user:p\:a\\ss` + "\n"
	if string(data) != want {
		t.Errorf("file content = %q, want %q", string(data), want)
	}
}

func TestWritePgpassEntry_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "pgpass.conf")
	t.Setenv("PGPASSFILE", path)

	cfg := &pgmi.ConnectionConfig{
		Host:     "localhost",
		Port:     5432,
		Database: "db",
		Username: "user",
		Password: "pass",
	}

	if err := writePgpassEntry(cfg); err != nil {
		t.Fatalf("writePgpassEntry() error = %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestPgpassPath_RespectsEnvVar(t *testing.T) {
	t.Setenv("PGPASSFILE", "/custom/path/pgpass")
	got := pgpassPath()
	if got != "/custom/path/pgpass" {
		t.Errorf("pgpassPath() = %q, want /custom/path/pgpass", got)
	}
}

func TestPgpassPath_DefaultWhenNoEnv(t *testing.T) {
	t.Setenv("PGPASSFILE", "")
	got := pgpassPath()
	if got == "" {
		t.Error("pgpassPath() returned empty string")
	}
}

func TestWritePgpassEntry_IsOwnerReadableOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file permissions not enforced on Windows")
	}

	t.Run("new file gets 0600", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "pgpass.conf")
		t.Setenv("PGPASSFILE", path)

		cfg := &pgmi.ConnectionConfig{
			Host: "h", Port: 5432, Database: "d", Username: "u", Password: "p",
		}
		if err := writePgpassEntry(cfg); err != nil {
			t.Fatalf("writePgpassEntry() error = %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("file permissions = %04o, want 0600", perm)
		}
	})

	t.Run("pre-existing wide tmp file does not widen result", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "pgpass.conf")
		tmpPath := path + ".pgmi-tmp"
		t.Setenv("PGPASSFILE", path)

		if err := os.WriteFile(tmpPath, []byte("stale"), 0644); err != nil {
			t.Fatal(err)
		}

		cfg := &pgmi.ConnectionConfig{
			Host: "h", Port: 5432, Database: "d", Username: "u", Password: "p",
		}
		if err := writePgpassEntry(cfg); err != nil {
			t.Fatalf("writePgpassEntry() error = %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("file permissions = %04o, want 0600", perm)
		}
		if _, err := os.Stat(tmpPath); err == nil {
			t.Error("tmp file should be cleaned up after rename")
		}
	})
}

// libpq reads .pgpass top-down and uses the first line whose host, port,
// database and user match, wildcards included. Verified against libpq 17: with
// `*:*:*:postgres:WRONG` above a correct specific line, psql fails with
// "password authentication failed ... password retrieved from file"; reversing
// the two lines connects. So an entry written below a matching wildcard is
// dead, and pgmi would still have printed "Saved to ...".
func TestWritePgpassEntry_OutranksAMatchingWildcard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass.conf")
	t.Setenv("PGPASSFILE", path)

	existing := "# my passwords\n*:*:*:user:stalepass\nother:5432:db:u:p\n"
	if err := os.WriteFile(path, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &pgmi.ConnectionConfig{
		Host: "localhost", Port: 5432, Database: "testdb",
		Username: "user", Password: "freshpass",
	}
	if err := writePgpassEntry(cfg); err != nil {
		t.Fatalf("writePgpassEntry() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	if lines[0] != "localhost:5432:testdb:user:freshpass" {
		t.Errorf("saved entry is not the first match libpq will find; line 0 = %q", lines[0])
	}
	// The wildcard is the user's own entry for other hosts — override it here,
	// do not delete it.
	if !slices.Contains(lines, "*:*:*:user:stalepass") {
		t.Errorf("the user's wildcard entry was dropped: %q", lines)
	}
	if !slices.Contains(lines, "other:5432:db:u:p") {
		t.Errorf("an unrelated entry was dropped: %q", lines)
	}
}

// An exact-tuple entry must be replaced, not accumulated, however many times
// the password is saved.
func TestWritePgpassEntry_DoesNotAccumulateDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass.conf")
	t.Setenv("PGPASSFILE", path)

	cfg := &pgmi.ConnectionConfig{
		Host: "h", Port: 1, Database: "d", Username: "u", Password: "p1",
	}
	for _, pw := range []string{"p1", "p2", "p3"} {
		cfg.Password = pw
		if err := writePgpassEntry(cfg); err != nil {
			t.Fatalf("writePgpassEntry(%s) error = %v", pw, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line after 3 saves, got %d: %q", len(lines), lines)
	}
	if lines[0] != "h:1:d:u:p3" {
		t.Errorf("line = %q, want the last password saved", lines[0])
	}
}
