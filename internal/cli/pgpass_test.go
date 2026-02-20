package cli

import (
	"os"
	"path/filepath"
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
	if lines[0] != "otherhost:5432:otherdb:otheruser:oldpass" {
		t.Errorf("first line modified: %q", lines[0])
	}
	if lines[1] != "localhost:5432:testdb:user:newpass" {
		t.Errorf("second line = %q, want updated entry", lines[1])
	}
}

func TestWritePgpassEntry_AppendsNew(t *testing.T) {
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
	if lines[1] != "newhost:5433:newdb:newuser:newpass" {
		t.Errorf("appended line = %q", lines[1])
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
